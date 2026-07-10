-- Hourly pre-aggregation of monitor_results so 7d/30d stats don't scan raw rows.
-- One row per (monitor, hour) holding count + sum/min/max latency. The 24h stats
-- view keeps reading raw results; only longer windows read these rollups.

CREATE TABLE "monitor_result_rollups" (
    "monitor_id" TEXT NOT NULL,
    "bucket" TIMESTAMP(3) NOT NULL,
    "checks" INTEGER NOT NULL,
    "sum_latency" INTEGER NOT NULL,
    "min_latency" INTEGER NOT NULL,
    "max_latency" INTEGER NOT NULL,

    CONSTRAINT "monitor_result_rollups_pkey" PRIMARY KEY ("monitor_id", "bucket")
);
ALTER TABLE "monitor_result_rollups" ADD CONSTRAINT "monitor_result_rollups_monitor_id_fkey"
    FOREIGN KEY ("monitor_id") REFERENCES "monitors"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- Recompute the rollups for the recent window (default last 3 hours, covering the
-- still-open current hour + any late arrivals). Closed hours are immutable, so a
-- small lookback keeps this cheap; partition pruning limits the scan to recent
-- monitor_results partitions. Run frequently by the host timer; a NULL/large
-- lookback backfills history.
CREATE OR REPLACE FUNCTION maintenance.refresh_rollups(p_lookback interval DEFAULT interval '3 hours')
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO public.monitor_result_rollups
        (monitor_id, bucket, checks, sum_latency, min_latency, max_latency)
    SELECT monitor_id, date_trunc('hour', checked_at),
           count(*), sum(latency), min(latency), max(latency)
    FROM public.monitor_results
    WHERE p_lookback IS NULL
       OR checked_at >= date_trunc('hour', now()) - p_lookback
    GROUP BY monitor_id, date_trunc('hour', checked_at)
    ON CONFLICT (monitor_id, bucket) DO UPDATE
        SET checks      = EXCLUDED.checks,
            sum_latency = EXCLUDED.sum_latency,
            min_latency = EXCLUDED.min_latency,
            max_latency = EXCLUDED.max_latency;
END;
$$;

-- Backfill every existing hour.
SELECT maintenance.refresh_rollups(NULL);

-- Extend the daily maintenance orchestrator to also age out old rollups. Kept
-- far longer than raw results (cheap: one row per monitor-hour) so longer
-- lookback windows remain possible later.
CREATE OR REPLACE FUNCTION maintenance.run_maintenance()
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM maintenance.ensure_month_partitions('public.monitor_results',
        date_trunc('month', now())::date,
        (date_trunc('month', now()) + interval '2 month')::date);
    PERFORM maintenance.ensure_month_partitions('public.alert_history',
        date_trunc('month', now())::date,
        (date_trunc('month', now()) + interval '2 month')::date);

    PERFORM maintenance.drop_old_partitions('public.monitor_results', interval '90 days');
    PERFORM maintenance.drop_old_partitions('public.alert_history',   interval '365 days');

    DELETE FROM public.monitor_result_rollups
    WHERE bucket < date_trunc('hour', now()) - interval '400 days';
END;
$$;
