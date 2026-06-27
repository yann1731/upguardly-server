-- Partition the two append-only timeseries tables (monitor_results, alert_history)
-- by time, with automated create-ahead + retention. This is RAW SQL because
-- Prisma Migrate cannot author partitioned-table DDL.
--
-- Key design points so the Prisma drift check stays green:
--   * The PARTITIONED PARENT tables stay in `public` with the SAME columns,
--     index names and FK names Prisma expects. The PK gains the partition key
--     (Postgres requires it): monitor_results -> (id, checked_at),
--     alert_history -> (id, sent_at). schema.prisma uses the matching @@id.
--   * Partition CHILD tables and the maintenance functions live in a separate
--     `maintenance` schema, which Prisma's datasource (public only) never
--     introspects — so they don't show up as drift.
--   * A DEFAULT partition guarantees inserts never fail even if the maintenance
--     job lags (the writers rely on the checked_at/sent_at DEFAULT now(), so a
--     partition must always exist for "now").
--
-- NOTE: converting an existing populated table copies all rows under locks held
-- for the migration's transaction. Treat a deploy that includes this as a
-- maintenance-window operation on a hot table. See docs/runbooks/partitioning.md.

CREATE SCHEMA IF NOT EXISTS maintenance;

-- ---------------------------------------------------------------------------
-- Maintenance functions (live in the `maintenance` schema; invisible to Prisma)
-- ---------------------------------------------------------------------------

-- Create monthly RANGE partitions for every month in [p_from, p_to], if absent.
-- Children are created in the `maintenance` schema, named <parent>_YYYY_MM.
CREATE OR REPLACE FUNCTION maintenance.ensure_month_partitions(
    p_parent regclass,
    p_from   date,
    p_to     date
) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    m        date;
    last_m   date := date_trunc('month', p_to)::date;
    short    text;
    child    text;
BEGIN
    SELECT relname INTO short FROM pg_class WHERE oid = p_parent;
    m := date_trunc('month', p_from)::date;
    WHILE m <= last_m LOOP
        child := short || '_' || to_char(m, 'YYYY_MM');
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS maintenance.%I PARTITION OF %s '
            || 'FOR VALUES FROM (%L) TO (%L)',
            child, p_parent,
            to_char(m, 'YYYY-MM-DD'),
            to_char((m + interval '1 month')::date, 'YYYY-MM-DD')
        );
        m := (m + interval '1 month')::date;
    END LOOP;
END;
$$;

-- Drop monthly partitions whose month is entirely older than the retention
-- window. The DEFAULT partition and any non-month-named children are skipped.
CREATE OR REPLACE FUNCTION maintenance.drop_old_partitions(
    p_parent    regclass,
    p_retention interval
) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    cutoff date := date_trunc('month', now() - p_retention)::date;
    r      record;
BEGIN
    FOR r IN
        SELECT n.nspname, c.relname
        FROM pg_inherits i
        JOIN pg_class c     ON c.oid = i.inhrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE i.inhparent = p_parent
          AND c.relname ~ '_[0-9]{4}_[0-9]{2}$'
    LOOP
        IF to_date(right(r.relname, 7), 'YYYY_MM') < cutoff THEN
            EXECUTE format('DROP TABLE %I.%I', r.nspname, r.relname);
        END IF;
    END LOOP;
END;
$$;

-- Orchestrator called daily by the host timer (and once at the end of this
-- migration). Retention windows are defined here — change them with a new
-- migration or CREATE OR REPLACE. monitor_results: 90 days. alert_history
-- (write-only audit log): 365 days.
CREATE OR REPLACE FUNCTION maintenance.run_maintenance()
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    -- Always keep the current month + 2 months ahead so writers have a home.
    PERFORM maintenance.ensure_month_partitions('public.monitor_results',
        date_trunc('month', now())::date,
        (date_trunc('month', now()) + interval '2 month')::date);
    PERFORM maintenance.ensure_month_partitions('public.alert_history',
        date_trunc('month', now())::date,
        (date_trunc('month', now()) + interval '2 month')::date);

    PERFORM maintenance.drop_old_partitions('public.monitor_results', interval '90 days');
    PERFORM maintenance.drop_old_partitions('public.alert_history',   interval '365 days');
END;
$$;

-- ---------------------------------------------------------------------------
-- monitor_results -> partitioned by checked_at
-- ---------------------------------------------------------------------------
ALTER TABLE "monitor_results" RENAME TO "monitor_results_old";
-- Free the index names (PK index + secondary index) for reuse on the new table;
-- index names are unique per schema. The old table keeps its FK (FK names are
-- per-table, so no clash with the new table's identically-named FK).
ALTER TABLE "monitor_results_old" DROP CONSTRAINT "monitor_results_pkey";
DROP INDEX "monitor_results_monitor_id_checked_at_idx";

CREATE TABLE "monitor_results" (
    "id" TEXT NOT NULL,
    "monitor_id" TEXT NOT NULL,
    "status" "Status" NOT NULL,
    "latency" INTEGER NOT NULL,
    "status_code" INTEGER,
    "message" TEXT,
    "checked_at" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "monitor_results_pkey" PRIMARY KEY ("id", "checked_at")
) PARTITION BY RANGE ("checked_at");

CREATE INDEX "monitor_results_monitor_id_checked_at_idx"
    ON "monitor_results"("monitor_id", "checked_at");
ALTER TABLE "monitor_results" ADD CONSTRAINT "monitor_results_monitor_id_fkey"
    FOREIGN KEY ("monitor_id") REFERENCES "monitors"("id")
    ON DELETE CASCADE ON UPDATE CASCADE;

CREATE TABLE maintenance."monitor_results_default"
    PARTITION OF "monitor_results" DEFAULT;

-- ---------------------------------------------------------------------------
-- alert_history -> partitioned by sent_at
-- ---------------------------------------------------------------------------
ALTER TABLE "alert_history" RENAME TO "alert_history_old";
ALTER TABLE "alert_history_old" DROP CONSTRAINT "alert_history_pkey";
DROP INDEX "alert_history_alert_id_sent_at_idx";

CREATE TABLE "alert_history" (
    "id" TEXT NOT NULL,
    "alert_id" TEXT NOT NULL,
    "status" "Status" NOT NULL,
    "message" TEXT NOT NULL,
    "sent_at" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "alert_history_pkey" PRIMARY KEY ("id", "sent_at")
) PARTITION BY RANGE ("sent_at");

CREATE INDEX "alert_history_alert_id_sent_at_idx"
    ON "alert_history"("alert_id", "sent_at");
ALTER TABLE "alert_history" ADD CONSTRAINT "alert_history_alert_id_fkey"
    FOREIGN KEY ("alert_id") REFERENCES "alerts"("id")
    ON DELETE CASCADE ON UPDATE CASCADE;

CREATE TABLE maintenance."alert_history_default"
    PARTITION OF "alert_history" DEFAULT;

-- ---------------------------------------------------------------------------
-- Pre-create month partitions covering existing data, then copy rows across so
-- nothing lands in the DEFAULT partition. Finally drop the old tables.
-- ---------------------------------------------------------------------------
DO $$
DECLARE d_min date;
BEGIN
    SELECT date_trunc('month', min("checked_at"))::date INTO d_min FROM "monitor_results_old";
    IF d_min IS NOT NULL THEN
        PERFORM maintenance.ensure_month_partitions('public.monitor_results',
            d_min, (date_trunc('month', now()) + interval '2 month')::date);
    END IF;

    SELECT date_trunc('month', min("sent_at"))::date INTO d_min FROM "alert_history_old";
    IF d_min IS NOT NULL THEN
        PERFORM maintenance.ensure_month_partitions('public.alert_history',
            d_min, (date_trunc('month', now()) + interval '2 month')::date);
    END IF;
END $$;

INSERT INTO "monitor_results" SELECT * FROM "monitor_results_old";
DROP TABLE "monitor_results_old";

INSERT INTO "alert_history" SELECT * FROM "alert_history_old";
DROP TABLE "alert_history_old";

-- Ensure current + future partitions exist and apply retention immediately.
SELECT maintenance.run_maintenance();
