-- Follow-plan check intervals. A monitor's interval becomes nullable: NULL
-- means "follow the owner's plan", resolved to the plan's minimum interval at
-- read time instead of being frozen into the row at create time. Plan
-- upgrades/downgrades then take effect on existing monitors automatically,
-- with no per-monitor writes and no dependence on a reconcile webhook firing.
--
-- The plan floors are single-sourced in Go (models.LimitsForPlan). SQL needs
-- them in exactly one place — the quorum function's freshness window, which is
-- single-statement-by-design and cannot call back into Go — so this migration
-- adds a tiny helper that duplicates the two floors. KEEP IN SYNC with
-- models.LimitsForPlan (internal/models/plan.go); the scheduler-integration
-- test pins them equal.
--
-- This migration is schema-only and safe under a not-yet-upgraded binary
-- (nullable column with no NULL rows yet). The NULL backfill is a separate,
-- later migration (20260707130000) that must run only after every scheduler
-- binary understands a NULL interval.

ALTER TABLE "monitors"
    ALTER COLUMN "interval" DROP NOT NULL,
    ALTER COLUMN "interval" DROP DEFAULT;

CREATE OR REPLACE FUNCTION maintenance.effective_interval(p_interval int, p_plan text)
RETURNS int
LANGUAGE sql IMMUTABLE AS $$
    -- NULL interval = follow plan. KEEP IN SYNC with models.LimitsForPlan.
    SELECT COALESCE(p_interval, CASE p_plan WHEN 'FREE' THEN 300 ELSE 60 END);
$$;

-- Re-create evaluate_monitor_quorum (unchanged from migration 20260706120000
-- except the freshness window): resolve the monitor owner's effective plan and
-- feed the interval through maintenance.effective_interval so a follow-plan
-- (NULL) monitor uses its current plan's floor. record_region_check is
-- untouched — it reads only regions, never interval.
CREATE OR REPLACE FUNCTION maintenance.evaluate_monitor_quorum(
    p_monitor_id       text,
    p_status_code      int,
    p_latency          int,
    p_up_message       text,
    p_stale_multiplier int DEFAULT 3,
    p_active_threshold interval DEFAULT interval '60 seconds'
) RETURNS TABLE (transition text, incident_id text)
LANGUAGE plpgsql AS $$
DECLARE
    v_mon         record;
    v_owner       text;
    v_plan        text;
    v_eff_int     int;
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
        RETURN QUERY SELECT 'none'::text, NULL::text;
        RETURN;
    END IF;

    -- Monitor owner (org owner for org monitors) is the billing subject and
    -- also the alert-channel owner below. Resolve once.
    v_owner := v_mon.user_id;
    IF v_mon.org_id IS NOT NULL THEN
        SELECT owner_id INTO v_owner FROM organizations WHERE id = v_mon.org_id;
        v_owner := COALESCE(v_owner, v_mon.user_id);
    END IF;

    -- Effective plan, mirroring handlers.effectivePlan: CANCELED (and terminal
    -- statuses that store CANCELED) carry no entitlement; missing row = FREE.
    SELECT CASE WHEN s.status = 'CANCELED' THEN 'FREE' ELSE s.plan::text END
      INTO v_plan
      FROM subscriptions s WHERE s.user_id = v_owner;
    IF v_plan IS NULL THEN
        v_plan := 'FREE';
    END IF;

    v_eff_int := maintenance.effective_interval(v_mon."interval", v_plan);
    v_fresh   := (v_eff_int * p_stale_multiplier) * interval '1 second';

    WITH active AS (
        SELECT region FROM scheduler_region_heartbeats
         WHERE last_seen_at >= now() - p_active_threshold
    ),
    fresh AS (
        SELECT rs.region, rs.status, rs.message
          FROM monitor_region_status rs
         WHERE rs.monitor_id = p_monitor_id
           AND rs.checked_at >= now() - CASE WHEN rs.source = 'VERIFICATION'
                                             THEN interval '120 seconds'
                                             ELSE v_fresh END
    ),
    reporters AS (
        SELECT f.region, f.status, f.message
          FROM fresh f
         WHERE f.region IN (SELECT region FROM active)
            OR f.region = ANY (v_mon.regions)
    ),
    pending AS (
        SELECT rvr.region
          FROM region_verification_requests rvr
         WHERE rvr.monitor_id = p_monitor_id
           AND rvr.expires_at >= now()
           AND rvr.region IN (SELECT region FROM active)
           AND rvr.region NOT IN (SELECT region FROM reporters)
    )
    SELECT
        (SELECT count(*) FROM reporters) + (SELECT count(*) FROM pending),
        (SELECT count(*) FROM reporters WHERE status <> 'UP'),
        (SELECT CASE WHEN bool_or(status = 'DOWN') THEN 'DOWN'::"Status"
                     ELSE 'DEGRADED'::"Status" END
           FROM reporters WHERE status <> 'UP'),
        (SELECT string_agg(region || ' ' || status || COALESCE(': ' || message, ''),
                           '; ' ORDER BY region)
           FROM reporters WHERE status <> 'UP')
      INTO v_total, v_unhealthy, v_worst, v_regions_msg;

    IF v_total < 1 THEN
        v_total := 1;
    END IF;

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
        v_out_message := p_up_message;
        DELETE FROM monitor_region_status
         WHERE monitor_id = p_monitor_id AND source = 'VERIFICATION';
        DELETE FROM region_verification_requests WHERE monitor_id = p_monitor_id;
    END IF;

    IF v_transition <> 'none' THEN
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
