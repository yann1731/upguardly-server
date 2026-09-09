-- Maintenance windows (ENTERPRISE): per-monitor time windows during which no
-- alerts are emitted. Checks still run, results and rollups still record, and
-- incidents still open/escalate/resolve — uptime stats stay honest; only
-- notification emission is suppressed (open and resolve alike, symmetrically).
--
-- Two window kinds cover the real use cases:
--   ONE_OFF — a concrete [starts_at, ends_at) UTC range (planned deploy);
--   WEEKLY  — every <weekday> at <start_time> for <duration_minutes>,
--             evaluated in an IANA <timezone> (nightly backup job).
--
-- This migration also extracts the alert fan-out from
-- maintenance.evaluate_monitor_quorum into maintenance.enqueue_monitor_alerts
-- so the repeat-alert sweep and expiry checks can reuse it. Its signature is
-- load-bearing for rolling deploys: never change it later without the
-- drop-first dance documented in 20260706120000 (old binaries call the
-- functions by name during the deploy window).

CREATE TABLE "maintenance_windows" (
    "id"               TEXT NOT NULL,
    "monitor_id"       TEXT NOT NULL,
    "kind"             TEXT NOT NULL,
    -- ONE_OFF bounds (UTC instants).
    "starts_at"        TIMESTAMPTZ,
    "ends_at"          TIMESTAMPTZ,
    -- WEEKLY recurrence: 0=Sunday .. 6=Saturday (Postgres EXTRACT(dow)
    -- convention), local wall-clock start, length, and the IANA zone the
    -- occurrence is evaluated in (midnight wrap handled at evaluation time).
    "weekday"          SMALLINT,
    "start_time"       TIME,
    "duration_minutes" INTEGER,
    "timezone"         TEXT,
    "created_at"       TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT "maintenance_windows_pkey" PRIMARY KEY ("id"),
    CONSTRAINT "maintenance_windows_kind_check" CHECK ("kind" IN ('ONE_OFF', 'WEEKLY')),
    -- Exactly the fields of the row's kind, nothing of the other's.
    CONSTRAINT "maintenance_windows_shape_check" CHECK (
        ("kind" = 'ONE_OFF'
            AND "starts_at" IS NOT NULL AND "ends_at" IS NOT NULL AND "ends_at" > "starts_at"
            AND "weekday" IS NULL AND "start_time" IS NULL
            AND "duration_minutes" IS NULL AND "timezone" IS NULL)
        OR
        ("kind" = 'WEEKLY'
            AND "weekday" BETWEEN 0 AND 6 AND "start_time" IS NOT NULL
            AND "duration_minutes" BETWEEN 1 AND 1440 AND "timezone" IS NOT NULL
            AND "starts_at" IS NULL AND "ends_at" IS NULL)
    )
);

CREATE INDEX "maintenance_windows_monitor_id_idx" ON "maintenance_windows"("monitor_id");

ALTER TABLE "maintenance_windows" ADD CONSTRAINT "maintenance_windows_monitor_id_fkey"
    FOREIGN KEY ("monitor_id") REFERENCES "monitors"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- True while the monitor is inside any of its maintenance windows. Evaluated
-- live at alert-emission time (no scheduler job restart needed when windows
-- change). A WEEKLY occurrence can wrap past local midnight, so yesterday's
-- occurrence is tested as well as today's.
CREATE OR REPLACE FUNCTION maintenance.in_maintenance(p_monitor_id text)
RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1
          FROM maintenance_windows w
         CROSS JOIN LATERAL (SELECT now() AT TIME ZONE w.timezone AS local) l
         CROSS JOIN LATERAL (
             SELECT date_trunc('day', l.local) + w.start_time                     AS today_start,
                    date_trunc('day', l.local) - interval '1 day' + w.start_time  AS yday_start
         ) o
         WHERE w.monitor_id = p_monitor_id
           AND (
               (w.kind = 'ONE_OFF' AND now() >= w.starts_at AND now() < w.ends_at)
            OR (w.kind = 'WEEKLY' AND (
                   (EXTRACT(dow FROM l.local)::int = w.weekday
                    AND l.local >= o.today_start
                    AND l.local <  o.today_start + w.duration_minutes * interval '1 minute')
                OR (EXTRACT(dow FROM l.local - interval '1 day')::int = w.weekday
                    AND l.local >= o.yday_start
                    AND l.local <  o.yday_start + w.duration_minutes * interval '1 minute')
               ))
           )
    );
$$;

-- Fan an alert out to the monitor's destinations: the owner's enabled
-- notification channels (honouring per-monitor monitor_channel_settings
-- overrides) and, for org monitors, the org's notify-only alert recipients
-- (skipping duplicates of an effectively-enabled owner channel). Extracted
-- verbatim from evaluate_monitor_quorum (KEEP IN SYNC with the effective-
-- enablement logic in monitor_channels.go). Returns the number of outbox rows
-- inserted so callers can tell whether anything actually went out.
CREATE OR REPLACE FUNCTION maintenance.enqueue_monitor_alerts(
    p_monitor_id  text,
    p_status      "Status",
    p_message     text,
    p_status_code int,
    p_latency     int
) RETURNS int
LANGUAGE plpgsql AS $$
DECLARE
    v_mon   record;
    v_owner text;
    v_count int := 0;
    v_n     int;
BEGIN
    SELECT id, user_id, org_id, name, type, target
      INTO v_mon
      FROM monitors WHERE id = p_monitor_id;
    IF NOT FOUND THEN
        RETURN 0;
    END IF;

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
           p_status, p_message, p_status_code, p_latency,
           v_mon.name, v_mon.type, v_mon.target, 0, now(), now()
      FROM notification_channels ch
      LEFT JOIN monitor_channel_settings mcs
             ON mcs.notification_channel_id = ch.id
            AND mcs.monitor_id = p_monitor_id
     WHERE ch.user_id = v_owner
       AND COALESCE(mcs.enabled, ch.enabled);
    GET DIAGNOSTICS v_n = ROW_COUNT;
    v_count := v_count + v_n;

    -- Org alert recipients (notify-only seats). Skip a recipient that
    -- duplicates an owner channel effectively enabled for this monitor — the
    -- INSERT above already covers that destination.
    IF v_mon.org_id IS NOT NULL THEN
        INSERT INTO alert_outbox
               (id, org_alert_recipient_id, monitor_id, channel, target, status,
                message, status_code, latency, monitor_name, monitor_type,
                monitor_target, attempts, next_attempt_at, created_at)
        SELECT gen_random_uuid()::text, r.id, p_monitor_id, r.channel, r.target,
               p_status, p_message, p_status_code, p_latency,
               v_mon.name, v_mon.type, v_mon.target, 0, now(), now()
          FROM org_alert_recipients r
         WHERE r.organization_id = v_mon.org_id
           AND NOT EXISTS (
               SELECT 1 FROM notification_channels ch
                 LEFT JOIN monitor_channel_settings mcs
                        ON mcs.notification_channel_id = ch.id
                       AND mcs.monitor_id = p_monitor_id
                WHERE ch.user_id = v_owner
                  AND ch.channel = r.channel
                  AND ch.target = r.target
                  AND COALESCE(mcs.enabled, ch.enabled));
        GET DIAGNOSTICS v_n = ROW_COUNT;
        v_count := v_count + v_n;
    END IF;

    RETURN v_count;
END;
$$;

-- Re-create evaluate_monitor_quorum: full definition copied verbatim from
-- migration 20260714120000 (future edits: copy from here), with the inline
-- outbox fan-out replaced by maintenance.enqueue_monitor_alerts and wrapped in
-- the maintenance-window gate. Suppression happens here — at the moment the
-- alert is born, atomically with the transition decision — so the dispatcher
-- stays a dumb delivery loop and rows already in the outbox keep delivering.
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
        PERFORM maintenance.enqueue_monitor_alerts(
            p_monitor_id, v_out_status, v_out_message, p_status_code, p_latency);
    END IF;

    RETURN QUERY SELECT v_transition, v_incident_id;
END;
$$;
