-- Enterprise org seats: alerting seats become org-level notify-only alert
-- recipients. A recipient is a bare contact (EMAIL address or SMS phone
-- number) attached to an organization — no user account involved. Recipients
-- receive alerts for every org monitor, alongside the org owner's own
-- notification channels (the owner receives by default and does not consume a
-- seat). The 3-recipient Enterprise cap is enforced in the API layer
-- (models.LimitsForPlan MaxAlertRecipients), not here.
--
-- Login seats (owner + 3 invited members) are pure application logic; this
-- migration carries no schema for them.
--
-- Deploy compatibility: an old scheduler binary handles recipient-sourced
-- outbox rows correctly — delivery reads the row's self-contained
-- channel/target, and FinalizeOutboxAlert with neither alert_id nor
-- notification_channel_id simply deletes the row (no history). Do not add a
-- NOT NULL or RETURNING dependency on org_alert_recipient_id later without
-- revisiting that.

CREATE TABLE "org_alert_recipients" (
    "id" TEXT NOT NULL,
    "organization_id" TEXT NOT NULL,
    "channel" "AlertChannel" NOT NULL,
    "target" TEXT NOT NULL,
    "created_at" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "org_alert_recipients_pkey" PRIMARY KEY ("id"),
    -- Notify-only contacts are reachable destinations, not dashboards:
    -- only EMAIL and SMS make sense (Slack/Discord/Telegram are the owner's
    -- integrations, configured as notification channels).
    CONSTRAINT "org_alert_recipients_channel_check" CHECK ("channel" IN ('EMAIL', 'SMS'))
);

CREATE UNIQUE INDEX "org_alert_recipients_organization_id_channel_target_key"
    ON "org_alert_recipients"("organization_id", "channel", "target");

ALTER TABLE "org_alert_recipients" ADD CONSTRAINT "org_alert_recipients_organization_id_fkey"
    FOREIGN KEY ("organization_id") REFERENCES "organizations"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- Outbox rows may now originate from a third source: an org alert recipient.
-- Deleting a recipient drops their queued alerts; a claimed in-flight row
-- still delivers from its own channel/target copy and finalizes as a plain
-- delete.
ALTER TABLE "alert_outbox" ADD COLUMN "org_alert_recipient_id" TEXT;
ALTER TABLE "alert_outbox" ADD CONSTRAINT "alert_outbox_org_alert_recipient_id_fkey"
    FOREIGN KEY ("org_alert_recipient_id") REFERENCES "org_alert_recipients"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- Exactly one source per outbox row, now three-way. alert_history keeps its
-- two-way check on purpose: recipient sends are not recorded in history.
ALTER TABLE "alert_outbox" DROP CONSTRAINT "alert_outbox_source_check";
ALTER TABLE "alert_outbox" ADD CONSTRAINT "alert_outbox_source_check"
    CHECK (num_nonnulls("alert_id", "notification_channel_id", "org_alert_recipient_id") = 1);

-- Re-create evaluate_monitor_quorum: full definition copied verbatim from
-- migration 20260707120000 (future edits: copy from here), plus one addition —
-- on any incident transition of an org monitor, also fan out to the org's
-- alert recipients. A recipient whose (channel, target) duplicates an owner
-- channel effectively enabled for this monitor is skipped to avoid
-- double-sending.
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

        -- Org alert recipients (notify-only seats): every incident transition
        -- of an org monitor also notifies the org's recipients. Skip a
        -- recipient that duplicates an owner channel effectively enabled for
        -- this monitor — the INSERT above already covers that destination.
        IF v_mon.org_id IS NOT NULL THEN
            INSERT INTO alert_outbox
                   (id, org_alert_recipient_id, monitor_id, channel, target, status,
                    message, status_code, latency, monitor_name, monitor_type,
                    monitor_target, attempts, next_attempt_at, created_at)
            SELECT gen_random_uuid()::text, r.id, p_monitor_id, r.channel, r.target,
                   v_out_status, v_out_message, p_status_code, p_latency,
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
        END IF;
    END IF;

    RETURN QUERY SELECT v_transition, v_incident_id;
END;
$$;
