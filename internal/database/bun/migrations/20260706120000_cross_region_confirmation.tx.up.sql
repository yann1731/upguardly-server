-- On-demand cross-region alert confirmation. Alerting no longer trusts a
-- single region's failure: when a scheduled check fails, every other active
-- region runs a one-off confirmation check, and an incident opens only when a
-- majority of the *active* regions (not just the monitor's configured ones)
-- agree the monitor is unhealthy. This turns the false-positive guard that
-- used to require a monitor be configured for multiple regions into a
-- platform-wide behavior that also protects single-region (FREE) monitors.
--
-- Two new tables give the quorum function a runtime view it could not get from
-- deployment config (AVAILABLE_REGIONS lives on the API server, not in the DB):
--   * scheduler_region_heartbeats — which regions currently have a live pool.
--   * region_verification_requests — a DB queue of one-off confirmation checks,
--     drained per-region with FOR UPDATE SKIP LOCKED, same pattern as
--     alert_outbox.
-- The quorum decision moves into maintenance.evaluate_monitor_quorum;
-- record_region_check upserts the region's status, enqueues confirmations, and
-- delegates to it.

-- 1. Live-region registry. Every scheduler instance upserts its region row on
--    a short timer; a region is "active" if its heartbeat is recent. A single
--    row per region (many instances in a region just keep bumping it).
CREATE TABLE "scheduler_region_heartbeats" (
    "region"       TEXT NOT NULL,
    "last_seen_at" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "scheduler_region_heartbeats_pkey" PRIMARY KEY ("region")
);

-- 2. One-off confirmation checks. One row per (monitor, region) in flight; the
--    region asked to verify claims it with SKIP LOCKED, runs the check, records
--    it as a VERIFICATION-source result, then deletes the row. expires_at
--    bounds how long a non-responding region can hold up the decision.
CREATE TABLE "region_verification_requests" (
    "id"           TEXT NOT NULL DEFAULT gen_random_uuid()::text,
    "monitor_id"   TEXT NOT NULL,
    "region"       TEXT NOT NULL,
    "requested_at" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "expires_at"   TIMESTAMP(3) NOT NULL,
    "claimed_at"   TIMESTAMP(3),
    "claimed_by"   TEXT,

    CONSTRAINT "region_verification_requests_pkey" PRIMARY KEY ("id"),
    CONSTRAINT "region_verification_requests_monitor_region_key" UNIQUE ("monitor_id", "region"),
    CONSTRAINT "region_verification_requests_monitor_id_fkey" FOREIGN KEY ("monitor_id")
        REFERENCES "monitors"("id") ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE INDEX "rvr_region_claim_idx"
    ON "region_verification_requests" ("region", "claimed_at", "expires_at");

-- 3. Distinguish a monitor's own configured-region reports (SCHEDULED) from
--    one-off confirmations produced by regions that don't normally check it
--    (VERIFICATION). Derived from configured-region membership at write time —
--    a confirmation that happens to come from a configured region is still
--    SCHEDULED. Lets the per-region status UI ignore verification noise and
--    lets resolve purge it. Existing rows default to SCHEDULED.
ALTER TABLE "monitor_region_status" ADD COLUMN "source" TEXT NOT NULL DEFAULT 'SCHEDULED';

-- 4. Quorum evaluation, split out of record_region_check so the expiry sweep
--    can re-evaluate a monitor without a triggering check. Same per-monitor
--    advisory lock (xact-scoped, re-entrant — safe to call from within
--    record_region_check, which already holds it) and same
--    single-statement-friendly structure as before.
--
--    Evaluation set (the quorum denominator):
--      active     = regions whose heartbeat is within p_active_threshold
--      fresh(r)   = a status row within interval*stale_multiplier, or within
--                   120s for VERIFICATION rows (one-off checks age out fast so
--                   a stale confirmation can't linger in a later episode)
--      reporters  = fresh rows for regions that are active OR configured
--      pending    = active regions with an unexpired verification request that
--                   have not reported yet
--      total      = |reporters ∪ pending|;  unhealthy = reporters not UP
--    DOWN when unhealthy*2 > total. A configured region with a dead pool goes
--    stale and drops out of the denominator (it no longer suppresses alerts —
--    a deliberate change from the pre-confirmation behavior).
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

    v_fresh := (v_mon."interval" * p_stale_multiplier) * interval '1 second';

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

    -- A monitor that just upserted a fresh row always has >= 1 reporter; guard
    -- only the standalone (expiry-driven) path where everything may be stale.
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
        v_out_message := p_up_message;
        -- Purge verification artifacts so a stale confirmation DOWN can't
        -- combine with a later single-region blip into a false reopen.
        DELETE FROM monitor_region_status
         WHERE monitor_id = p_monitor_id AND source = 'VERIFICATION';
        DELETE FROM region_verification_requests WHERE monitor_id = p_monitor_id;
    END IF;

    IF v_transition <> 'none' THEN
        -- Effective channels for this monitor: the owner's global channels with
        -- per-monitor overrides (absent row = inherit). An org monitor uses the
        -- org owner's channels. KEEP IN SYNC with the channel semantics in
        -- internal/api/handlers/monitor_channels.go.
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

-- 5. record_region_check: upsert this region's status, enqueue confirmations,
--    then delegate the decision. Recreated (not CREATE OR REPLACE) with two new
--    trailing defaulted params — the old 6-arg call from a not-yet-upgraded
--    scheduler binary still resolves during a rolling deploy. Dropped first so
--    the added parameter can't create an ambiguous overload.
DROP FUNCTION IF EXISTS maintenance.record_region_check(text, text, "Status", int, int, text, int);

CREATE OR REPLACE FUNCTION maintenance.record_region_check(
    p_monitor_id       text,
    p_region           text,
    p_status           "Status",
    p_latency          int,
    p_status_code      int,
    p_message          text,
    p_stale_multiplier int DEFAULT 3,
    p_source           text DEFAULT 'SCHEDULED'
) RETURNS TABLE (transition text, incident_id text)
LANGUAGE plpgsql AS $$
DECLARE
    v_regions    text[];
    v_row_source text;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(p_monitor_id, 0));

    SELECT regions INTO v_regions FROM monitors WHERE id = p_monitor_id;
    IF NOT FOUND THEN
        RETURN QUERY SELECT 'none'::text, NULL::text;
        RETURN;
    END IF;

    -- Stored source reflects whether this region normally checks the monitor,
    -- independent of whether *this* call came from the verifier.
    v_row_source := CASE WHEN p_region = ANY (v_regions) THEN 'SCHEDULED' ELSE 'VERIFICATION' END;

    INSERT INTO monitor_region_status AS s
           (monitor_id, region, status, latency, status_code, message, source, checked_at)
    VALUES (p_monitor_id, p_region, p_status, p_latency, p_status_code, p_message, v_row_source, now())
    ON CONFLICT (monitor_id, region) DO UPDATE
        SET status      = EXCLUDED.status,
            latency     = EXCLUDED.latency,
            status_code = EXCLUDED.status_code,
            message     = EXCLUDED.message,
            source      = EXCLUDED.source,
            checked_at  = EXCLUDED.checked_at;

    -- A failing scheduled check asks every other active region to confirm.
    -- Verification-origin checks never enqueue (no feedback loop). The 30s
    -- recency filter keeps a persistently-down monitor from re-enqueuing on
    -- every check. 60s active window: KEEP IN SYNC with p_active_threshold in
    -- evaluate_monitor_quorum.
    IF p_source = 'SCHEDULED' AND p_status <> 'UP' THEN
        INSERT INTO region_verification_requests (id, monitor_id, region, requested_at, expires_at)
        SELECT gen_random_uuid()::text, p_monitor_id, h.region, now(), now() + interval '60 seconds'
          FROM scheduler_region_heartbeats h
         WHERE h.last_seen_at >= now() - interval '60 seconds'
           AND h.region <> p_region
           AND NOT EXISTS (
               SELECT 1 FROM monitor_region_status rs
                WHERE rs.monitor_id = p_monitor_id
                  AND rs.region = h.region
                  AND rs.checked_at >= now() - interval '30 seconds')
        ON CONFLICT (monitor_id, region) DO NOTHING;
    END IF;

    RETURN QUERY SELECT *
      FROM maintenance.evaluate_monitor_quorum(
               p_monitor_id, p_status_code, p_latency, p_message, p_stale_multiplier);
END;
$$;
