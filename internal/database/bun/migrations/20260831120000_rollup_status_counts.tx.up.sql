-- Per-status counts on the hourly rollups, so availability (not just latency)
-- can be served from them.
--
-- The rollups held only checks/sum/min/max latency, which meant uptime had no
-- server-side source at all: the dashboard's "last 31 days" strip was built in
-- the browser from GET /monitors/:id/results, capped at 100 rows — roughly 25
-- minutes of history for a multi-region 60s monitor, so only the current day
-- ever painted. Counting UP/DEGRADED here makes GET /monitors/:id/uptime a
-- cheap read over one row per (monitor, region, hour).

-- Stored as two counts rather than one pre-summed "available" so whether
-- DEGRADED counts as available stays a query-level decision. `checks` remains
-- the denominator; DOWN is implied (checks - up_checks - degraded_checks).
ALTER TABLE "monitor_result_rollups"
    ADD COLUMN "up_checks" INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "monitor_result_rollups"
    ADD COLUMN "degraded_checks" INTEGER NOT NULL DEFAULT 0;

-- refresh_rollups gains the two counts. Body copied from the region-aware
-- definition in 20260703120000_add_regions, not the original in
-- 20260627130000_add_result_rollups.
CREATE OR REPLACE FUNCTION maintenance.refresh_rollups(p_lookback interval DEFAULT interval '3 hours')
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO public.monitor_result_rollups
        (monitor_id, region, bucket, checks, sum_latency, min_latency, max_latency,
         up_checks, degraded_checks)
    SELECT monitor_id, region, date_trunc('hour', checked_at),
           count(*), sum(latency), min(latency), max(latency),
           count(*) FILTER (WHERE status = 'UP'),
           count(*) FILTER (WHERE status = 'DEGRADED')
    FROM public.monitor_results
    WHERE p_lookback IS NULL
       OR checked_at >= date_trunc('hour', now()) - p_lookback
    GROUP BY monitor_id, region, date_trunc('hour', checked_at)
    ON CONFLICT (monitor_id, region, bucket) DO UPDATE
        SET checks          = EXCLUDED.checks,
            sum_latency     = EXCLUDED.sum_latency,
            min_latency     = EXCLUDED.min_latency,
            max_latency     = EXCLUDED.max_latency,
            up_checks       = EXCLUDED.up_checks,
            degraded_checks = EXCLUDED.degraded_checks;
END;
$$;

-- Backfill the new counts for every hour still reconstructible. Rollups are
-- kept 400 days but raw monitor_results only 90, so rollup rows older than the
-- raw retention keep up_checks = 0 and would read as 0% available. The uptime
-- endpoint therefore clamps its window to 90 days; a future longer-window
-- feature must account for this rather than trusting these columns further back.
SELECT maintenance.refresh_rollups(NULL);
