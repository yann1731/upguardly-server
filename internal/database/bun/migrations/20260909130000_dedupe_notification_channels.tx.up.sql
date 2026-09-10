-- Integrations are unique per (account, channel, destination).
--
-- Nothing enforced this before, and the alert fan-out inserts one outbox row
-- per notification_channels row, so two integrations pointing at the same
-- destination delivered every alert twice. EMAIL is where users hit it: its
-- target is pinned server-side to the account email, so every add after the
-- first was necessarily an exact duplicate.
--
-- Existing duplicates are collapsed onto the oldest row of each group before
-- the index goes on. Only exact duplicates are touched: an account holding
-- two different SMS numbers keeps both (the new one-per-account cap on EMAIL
-- and SMS is enforced at configuration time, like every other plan limit, so
-- what is already configured keeps delivering).

CREATE TEMP TABLE channel_dupes ON COMMIT DROP AS
SELECT id AS loser_id, keeper_id, group_enabled
  FROM (
      SELECT id,
             first_value(id) OVER w AS keeper_id,
             bool_or(enabled) OVER w AS group_enabled
        FROM notification_channels
      WINDOW w AS (PARTITION BY user_id, channel, target
                       ORDER BY created_at, id
                       ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING)
  ) ranked
 WHERE id <> keeper_id;

-- Delivery must not stop because the enabled row of a pair was the loser.
UPDATE notification_channels nc
   SET enabled = true, updated_at = now()
  FROM channel_dupes d
 WHERE nc.id = d.keeper_id
   AND d.group_enabled
   AND NOT nc.enabled;

-- Both FKs are ON DELETE CASCADE, so re-point before deleting or the account
-- loses the delivery record of every alert sent through a duplicate.
UPDATE alert_history ah
   SET notification_channel_id = d.keeper_id
  FROM channel_dupes d
 WHERE ah.notification_channel_id = d.loser_id;

UPDATE alert_outbox ao
   SET notification_channel_id = d.keeper_id
  FROM channel_dupes d
 WHERE ao.notification_channel_id = d.loser_id;

-- Per-monitor overrides move across too, unless the survivor already carries
-- one for that monitor — its own setting is the more explicit intent, and
-- (monitor_id, notification_channel_id) is unique. The rest go with the row.
UPDATE monitor_channel_settings mcs
   SET notification_channel_id = d.keeper_id, updated_at = now()
  FROM channel_dupes d
 WHERE mcs.notification_channel_id = d.loser_id
   AND NOT EXISTS (
       SELECT 1 FROM monitor_channel_settings survivor
        WHERE survivor.monitor_id = mcs.monitor_id
          AND survivor.notification_channel_id = d.keeper_id);

DELETE FROM notification_channels nc
 USING channel_dupes d
 WHERE nc.id = d.loser_id;

CREATE UNIQUE INDEX "notification_channels_user_id_channel_target_key"
    ON "notification_channels"("user_id", "channel", "target");
