-- Per-monitor slow-response threshold (milliseconds). A check that comes back
-- UP but slower than this is classified DEGRADED by the scheduler. NULL means
-- "use the per-type default" (HTTP 2000 / PORT 1000 / PING 500 — see
-- models.DefaultDegradedThresholdMs); an explicit value is a plan-gated
-- override (PRO and ENTERPRISE only, models.PlanLimits.CustomDegradedThreshold).
ALTER TABLE "monitors" ADD COLUMN "degraded_threshold_ms" INTEGER;
