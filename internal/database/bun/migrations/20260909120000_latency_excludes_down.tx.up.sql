-- Latency aggregates ignore DOWN checks.
--
-- A failed check's `latency` is not a response time: it is how long the checker
-- waited before giving up. A ping monitor whose host reboots records the full
-- timeout (30000ms on a 30s timeout), which then landed in sum/min/max latency
-- and inflated the monitor's average and maximum response time for the whole
-- window. Only UP and DEGRADED checks measured a real round trip, so only those
-- feed the latency columns from here on.
--
-- `checks` still counts every check — it stays the uptime denominator. The
-- latency sample count is therefore up_checks + degraded_checks, which is what
-- the Go side divides sum_latency by (see computeStatsFromRollups). An hour of
-- nothing but DOWN checks contributes no latency at all: sum/min/max coalesce
-- to 0 and the row is skipped when the aggregates are recombined.
CREATE OR REPLACE FUNCTION maintenance.refresh_rollups(p_lookback interval DEFAULT interval '3 hours')
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO public.monitor_result_rollups
        (monitor_id, region, bucket, checks, sum_latency, min_latency, max_latency,
         up_checks, degraded_checks)
    SELECT monitor_id, region, date_trunc('hour', checked_at),
           count(*),
           coalesce(sum(latency) FILTER (WHERE status <> 'DOWN'), 0),
           coalesce(min(latency) FILTER (WHERE status <> 'DOWN'), 0),
           coalesce(max(latency) FILTER (WHERE status <> 'DOWN'), 0),
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

-- Rewrite every hour still reconstructible from raw results (90 days of
-- retention against 400 days of rollups). Rollup rows older than that keep the
-- latency they were written with, DOWN samples included — the same limit that
-- already applies to up_checks/degraded_checks from
-- 20260831120000_rollup_status_counts.
SELECT maintenance.refresh_rollups(NULL);
