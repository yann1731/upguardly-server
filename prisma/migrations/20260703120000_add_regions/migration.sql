-- Multi-region checking. Hand-written SQL (Prisma can't author partitioned-
-- table ALTERs or plpgsql): monitors gain the set of regions that check them,
-- results/rollups gain the region that produced them, and incident detection
-- moves from the scheduler's in-memory tracker into a single serialized
-- quorum function so any number of regions/instances can report for the same
-- monitor without duplicate incidents or alerts.

-- 1. Which regions check each monitor. Existing monitors backfill to the
--    default region. Operators whose primary region is not na-east run a
--    one-off UPDATE (see docs/runbooks/multi-region.md).
ALTER TABLE "monitors"
    ADD COLUMN "regions" TEXT[] NOT NULL DEFAULT ARRAY['na-east']::TEXT[];

-- 2. Which region produced each result. Partitioned parent: the ALTER
--    cascades to all children, and a constant default is applied lazily
--    (fast) rather than rewriting existing partitions.
ALTER TABLE "monitor_results"
    ADD COLUMN "region" TEXT NOT NULL DEFAULT 'na-east';

-- 3. Rollups become per (monitor, region, hour).
ALTER TABLE "monitor_result_rollups"
    ADD COLUMN "region" TEXT NOT NULL DEFAULT 'na-east';
ALTER TABLE "monitor_result_rollups"
    DROP CONSTRAINT "monitor_result_rollups_pkey";
ALTER TABLE "monitor_result_rollups"
    ADD CONSTRAINT "monitor_result_rollups_pkey"
    PRIMARY KEY ("monitor_id", "region", "bucket");

-- 4. refresh_rollups groups by region. Existing rollup rows already carry
--    'na-east' via the column default and stay valid; no backfill needed.
CREATE OR REPLACE FUNCTION maintenance.refresh_rollups(p_lookback interval DEFAULT interval '3 hours')
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO public.monitor_result_rollups
        (monitor_id, region, bucket, checks, sum_latency, min_latency, max_latency)
    SELECT monitor_id, region, date_trunc('hour', checked_at),
           count(*), sum(latency), min(latency), max(latency)
    FROM public.monitor_results
    WHERE p_lookback IS NULL
       OR checked_at >= date_trunc('hour', now()) - p_lookback
    GROUP BY monitor_id, region, date_trunc('hour', checked_at)
    ON CONFLICT (monitor_id, region, bucket) DO UPDATE
        SET checks      = EXCLUDED.checks,
            sum_latency = EXCLUDED.sum_latency,
            min_latency = EXCLUDED.min_latency,
            max_latency = EXCLUDED.max_latency;
END;
$$;

-- 5. Latest check outcome per (monitor, region) — the input to quorum
--    evaluation and the source for the per-region status UI.
CREATE TABLE "monitor_region_status" (
    "monitor_id"  TEXT NOT NULL,
    "region"      TEXT NOT NULL,
    "status"      "Status" NOT NULL,
    "latency"     INTEGER NOT NULL DEFAULT 0,
    "status_code" INTEGER,
    "message"     TEXT,
    "checked_at"  TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "monitor_region_status_pkey" PRIMARY KEY ("monitor_id", "region"),
    CONSTRAINT "monitor_region_status_monitor_id_fkey" FOREIGN KEY ("monitor_id")
        REFERENCES "monitors"("id") ON DELETE CASCADE ON UPDATE CASCADE
);

-- 6. Quorum evaluation. Called by the scheduler once per check, replacing the
--    old in-memory incidentTracker (internal/scheduler/incidents.go, deleted)
--    and the Go-side outbox enqueue. Design constraints, in order:
--
--    * The Prisma Go client only supports batch transactions (no
--      read-then-branch), so the whole evaluate-and-transition step must be a
--      single statement: this function.
--    * All writers for a monitor — every region, every instance, including
--      transient duplicates during partition handoff — serialize on a
--      per-monitor advisory lock and read the open incident inside it, so at
--      most one 'opened' per outage and exactly one 'resolved', by
--      construction.
--    * The lock is xact-scoped and the function is one statement, which makes
--      it safe under PgBouncer transaction pooling (a session-scoped
--      pg_advisory_lock would not be).
--    * alert_outbox rows are written in the same transaction as the incident
--      transition, closing the crash window the old two-transaction Go path
--      had.
--
--    Quorum: a monitor is down when unhealthy_count * 2 > configured_count.
--    Only configured, fresh (checked within p_stale_multiplier x interval)
--    rows count as unhealthy; an absent, stale, or de-configured region can
--    neither open nor block an incident by itself. With 2 regions this means
--    BOTH must fail — deliberate false-positive protection; the per-region
--    status UI is what surfaces single-region failures.
--
--    ids: incidents.id / alert_outbox.id have no DB default (cuid lives in
--    the Prisma client), so this function generates uuids — same precedent as
--    the scheduler's batched monitor_results insert.
CREATE OR REPLACE FUNCTION maintenance.record_region_check(
    p_monitor_id       text,
    p_region           text,
    p_status           "Status",
    p_latency          int,
    p_status_code      int,
    p_message          text,
    p_stale_multiplier int DEFAULT 3
) RETURNS TABLE (transition text, incident_id text)
LANGUAGE plpgsql AS $$
DECLARE
    v_mon         record;
    v_owner       text;
    v_fresh       interval;
    v_total       int;
    v_unhealthy   int;
    v_worst       "Status";
    v_regions_msg text;
    v_open        record;
    v_new_status  "Status";
    v_transition  text := 'none';
    v_incident_id text;
    v_out_status  "Status";
    v_out_message text;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(p_monitor_id, 0));

    SELECT id, regions, "interval", user_id, org_id, name, type, target
      INTO v_mon
      FROM monitors WHERE id = p_monitor_id;
    IF NOT FOUND THEN
        -- Monitor deleted between check and record; nothing to do.
        RETURN QUERY SELECT 'none'::text, NULL::text;
        RETURN;
    END IF;

    INSERT INTO monitor_region_status AS s
           (monitor_id, region, status, latency, status_code, message, checked_at)
    VALUES (p_monitor_id, p_region, p_status, p_latency, p_status_code, p_message, now())
    ON CONFLICT (monitor_id, region) DO UPDATE
        SET status      = EXCLUDED.status,
            latency     = EXCLUDED.latency,
            status_code = EXCLUDED.status_code,
            message     = EXCLUDED.message,
            checked_at  = EXCLUDED.checked_at;

    v_total := COALESCE(array_length(v_mon.regions, 1), 1);
    v_fresh := (v_mon."interval" * p_stale_multiplier) * interval '1 second';

    SELECT count(*),
           CASE WHEN bool_or(rs.status = 'DOWN') THEN 'DOWN'::"Status"
                ELSE 'DEGRADED'::"Status" END,
           string_agg(rs.region || ' ' || rs.status
                      || COALESCE(': ' || rs.message, ''), '; ' ORDER BY rs.region)
      INTO v_unhealthy, v_worst, v_regions_msg
      FROM monitor_region_status rs
     WHERE rs.monitor_id = p_monitor_id
       AND rs.region = ANY (v_mon.regions)
       AND rs.status <> 'UP'
       AND rs.checked_at >= now() - v_fresh;

    SELECT id, status INTO v_open
      FROM incidents
     WHERE monitor_id = p_monitor_id AND resolved_at IS NULL
     ORDER BY started_at DESC
     LIMIT 1;

    IF v_unhealthy * 2 > v_total THEN
        v_out_message := format('%s/%s regions unhealthy: %s', v_unhealthy, v_total, v_regions_msg);

        IF v_open.id IS NULL THEN
            v_incident_id := gen_random_uuid()::text;
            INSERT INTO incidents (id, monitor_id, status, status_code, message, started_at, created_at)
            VALUES (v_incident_id, p_monitor_id, v_worst, p_status_code, v_out_message, now(), now());
            v_transition := 'opened';
            v_out_status := v_worst;
        ELSE
            v_incident_id := v_open.id;
            -- Worst status is sticky: an incident that ever went DOWN stays DOWN.
            v_new_status := CASE WHEN v_open.status = 'DOWN' OR v_worst = 'DOWN'
                                 THEN 'DOWN'::"Status" ELSE v_worst END;
            UPDATE incidents
               SET status      = v_new_status,
                   message     = v_out_message,
                   status_code = COALESCE(p_status_code, status_code)
             WHERE id = v_open.id;
            IF v_new_status <> v_open.status THEN
                v_transition := 'escalated';
                v_out_status := v_new_status;
            END IF;
        END IF;
    ELSIF v_open.id IS NOT NULL THEN
        UPDATE incidents SET resolved_at = now() WHERE id = v_open.id;
        v_incident_id := v_open.id;
        v_transition  := 'resolved';
        v_out_status  := 'UP';
        v_out_message := p_message;
    END IF;

    IF v_transition <> 'none' THEN
        -- Effective channels for this monitor: the owner's global channels
        -- with per-monitor overrides (absent row = inherit). An org monitor
        -- uses the org owner's channels. KEEP IN SYNC with the client-facing
        -- channel semantics in internal/api/handlers/monitor_channels.go —
        -- this replaced checkRunner.effectiveGlobalChannels (runner.go).
        v_owner := v_mon.user_id;
        IF v_mon.org_id IS NOT NULL THEN
            SELECT owner_id INTO v_owner FROM organizations WHERE id = v_mon.org_id;
            v_owner := COALESCE(v_owner, v_mon.user_id);
        END IF;

        INSERT INTO alert_outbox
               (id, notification_channel_id, monitor_id, channel, target, status,
                message, status_code, latency, monitor_name, monitor_type,
                monitor_target, attempts, next_attempt_at, created_at)
        SELECT gen_random_uuid()::text, ch.id, p_monitor_id, ch.channel, ch.target,
               v_out_status, v_out_message, p_status_code, p_latency,
               v_mon.name, v_mon.type, v_mon.target, 0, now(), now()
          FROM notification_channels ch
          LEFT JOIN monitor_channel_settings mcs
                 ON mcs.notification_channel_id = ch.id
                AND mcs.monitor_id = p_monitor_id
         WHERE ch.user_id = v_owner
           AND COALESCE(mcs.enabled, ch.enabled);
    END IF;

    RETURN QUERY SELECT v_transition, v_incident_id;
END;
$$;
