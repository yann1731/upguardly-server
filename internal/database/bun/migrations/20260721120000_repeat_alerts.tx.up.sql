-- Repeat alerts (ENTERPRISE): while an incident stays open, re-send its alert
-- every repeat_alert_interval_secs, at most repeat_alert_max_count times.
-- Config lives on the monitor (NULL = feature off); progress lives on the
-- incident (repeats_sent / last_alert_at). The sweep is driven by the
-- scheduler's repeater loop calling maintenance.enqueue_repeat_alerts() —
-- FOR UPDATE SKIP LOCKED makes concurrent scheduler instances safe.
--
-- Side benefit: an incident opened during a maintenance window never got its
-- "opened" alert (last_alert_at stays NULL). Once the window ends, the sweep's
-- COALESCE(last_alert_at, started_at) clause delivers the first alert for
-- monitors with repeats configured.

ALTER TABLE "monitors" ADD COLUMN "repeat_alert_interval_secs" INTEGER;
ALTER TABLE "monitors" ADD COLUMN "repeat_alert_max_count"     INTEGER;

ALTER TABLE "incidents" ADD COLUMN "repeats_sent"  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE "incidents" ADD COLUMN "last_alert_at" TIMESTAMPTZ;

-- The sweep scans open incidents only; keep that cheap.
CREATE INDEX IF NOT EXISTS "incidents_open_idx" ON "incidents"("monitor_id") WHERE "resolved_at" IS NULL;

-- One pass of the repeat sweep. Locks due incident rows (SKIP LOCKED so other
-- instances skip them), fans the reminder out through the shared
-- maintenance.enqueue_monitor_alerts, and advances the repeat state. State
-- advances even when no destination is currently enabled (0 rows inserted) so
-- a channel-less monitor doesn't hot-loop. Returns the number of incidents
-- swept.
CREATE OR REPLACE FUNCTION maintenance.enqueue_repeat_alerts() RETURNS int
LANGUAGE plpgsql AS $$
DECLARE
    v_row   record;
    v_count int := 0;
BEGIN
    FOR v_row IN
        SELECT i.id AS incident_id, i.monitor_id, i.status, i.message, i.status_code
          FROM incidents i
          JOIN monitors m ON m.id = i.monitor_id
         WHERE i.resolved_at IS NULL
           AND m.repeat_alert_interval_secs IS NOT NULL
           AND m.repeat_alert_max_count IS NOT NULL
           AND i.repeats_sent < m.repeat_alert_max_count
           AND COALESCE(i.last_alert_at, i.started_at)
               + m.repeat_alert_interval_secs * interval '1 second' <= now()
           AND NOT maintenance.in_maintenance(i.monitor_id)
         ORDER BY i.started_at
           FOR UPDATE OF i SKIP LOCKED
    LOOP
        PERFORM maintenance.enqueue_monitor_alerts(
            v_row.monitor_id,
            v_row.status,
            'Still ' || v_row.status || COALESCE(': ' || v_row.message, ''),
            v_row.status_code,
            NULL);
        UPDATE incidents
           SET repeats_sent  = repeats_sent + 1,
               last_alert_at = now()
         WHERE id = v_row.incident_id;
        v_count := v_count + 1;
    END LOOP;
    RETURN v_count;
END;
$$;

-- Re-create evaluate_monitor_quorum: full definition copied verbatim from
-- migration 20260720120000 (future edits: copy from here), with one addition —
-- when a transition's alerts actually go out (not maintenance-suppressed,
-- >0 outbox rows), stamp the incident's last_alert_at so the repeat sweep
-- counts from the last real notification.
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

    IF v_transition <> 'none' AND NOT maintenance.in_maintenance(p_monitor_id) THEN
        IF maintenance.enqueue_monitor_alerts(
               p_monitor_id, v_out_status, v_out_message, p_status_code, p_latency) > 0
           AND v_incident_id IS NOT NULL THEN
            UPDATE incidents SET last_alert_at = now() WHERE id = v_incident_id;
        END IF;
    END IF;

    RETURN QUERY SELECT v_transition, v_incident_id;
END;
$$;
