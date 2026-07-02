-- Per-monitor alerts are retired: notification channels ("integrations") are
-- now the only alert destinations, with per-monitor enablement expressed as
-- monitor_channel_settings overrides. This migration folds every enabled
-- per-monitor alert into that model so nothing stops firing:
--
--   * one globally-disabled channel per distinct (owner, channel, target),
--     unless the owner already has an identical channel;
--   * a per-monitor override enabling that channel for the alert's monitor.
--
-- A globally-disabled channel with a per-monitor enable reproduces the old
-- semantics exactly: it fires for that monitor and no others.
--
-- The alerts table itself is kept (empty of meaning, no code reads or writes
-- it) because alert_history audit rows cascade-delete with their alert; a
-- later migration can drop the table together with that history.

-- Resolve each enabled alert to the user whose channels govern the monitor:
-- the org owner for org monitors, the creator otherwise (mirrors
-- effectiveGlobalChannels in the scheduler).
CREATE TEMPORARY TABLE "_alert_fold" AS
SELECT a."id"         AS alert_id,
       a."monitor_id" AS monitor_id,
       a."channel"    AS channel,
       a."target"     AS target,
       COALESCE(o."owner_id", m."user_id") AS owner_id
FROM "alerts" a
JOIN "monitors" m ON m."id" = a."monitor_id"
LEFT JOIN "organizations" o ON o."id" = m."org_id"
WHERE a."enabled";

-- One globally-disabled channel per distinct destination that doesn't already
-- exist for the owner.
INSERT INTO "notification_channels" ("id", "user_id", "channel", "target", "enabled", "created_at", "updated_at")
SELECT gen_random_uuid()::text, f.owner_id, f.channel, f.target, false, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM (SELECT DISTINCT owner_id, channel, target FROM "_alert_fold") f
WHERE NOT EXISTS (
    SELECT 1 FROM "notification_channels" nc
    WHERE nc."user_id" = f.owner_id
      AND nc."channel" = f.channel
      AND nc."target"  = f.target
);

-- Enable the matching channel for each monitor that had the alert. An
-- existing override is left alone (it already expresses a user choice).
INSERT INTO "monitor_channel_settings" ("id", "monitor_id", "notification_channel_id", "enabled", "created_at", "updated_at")
SELECT DISTINCT ON (f.monitor_id, nc."id")
       gen_random_uuid()::text, f.monitor_id, nc."id", true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM "_alert_fold" f
JOIN "notification_channels" nc
  ON nc."user_id" = f.owner_id
 AND nc."channel" = f.channel
 AND nc."target"  = f.target
ON CONFLICT ("monitor_id", "notification_channel_id") DO NOTHING;

DROP TABLE "_alert_fold";
