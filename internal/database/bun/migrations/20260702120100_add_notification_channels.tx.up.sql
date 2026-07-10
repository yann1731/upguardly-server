-- Global (account-level) alert channels. Channels are per-user only — an org
-- monitor resolves to the org owner's channels, mirroring how an org's
-- effective plan is its owner's plan (see Subscription in schema.prisma).
CREATE TABLE "notification_channels" (
    "id" TEXT NOT NULL,
    "user_id" TEXT NOT NULL,
    "channel" "AlertChannel" NOT NULL,
    "target" TEXT NOT NULL,
    "enabled" BOOLEAN NOT NULL DEFAULT true,
    "created_at" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMP(3) NOT NULL,

    CONSTRAINT "notification_channels_pkey" PRIMARY KEY ("id")
);

CREATE INDEX "notification_channels_user_id_idx" ON "notification_channels"("user_id");

-- Per-monitor opt-in/opt-out override of a global channel. A monitor inherits
-- every global channel's own enabled flag unless a row here overrides it;
-- deleting the row reverts to inherit.
CREATE TABLE "monitor_channel_settings" (
    "id" TEXT NOT NULL,
    "monitor_id" TEXT NOT NULL,
    "notification_channel_id" TEXT NOT NULL,
    "enabled" BOOLEAN NOT NULL,
    "created_at" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMP(3) NOT NULL,

    CONSTRAINT "monitor_channel_settings_pkey" PRIMARY KEY ("id")
);

CREATE UNIQUE INDEX "monitor_channel_settings_monitor_id_notification_channel_id_key"
    ON "monitor_channel_settings"("monitor_id", "notification_channel_id");

ALTER TABLE "monitor_channel_settings" ADD CONSTRAINT "monitor_channel_settings_monitor_id_fkey"
    FOREIGN KEY ("monitor_id") REFERENCES "monitors"("id") ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE "monitor_channel_settings" ADD CONSTRAINT "monitor_channel_settings_notification_channel_id_fkey"
    FOREIGN KEY ("notification_channel_id") REFERENCES "notification_channels"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- Outbox/history rows may now originate from either a per-monitor alert or a
-- global notification channel: exactly one of (alert_id,
-- notification_channel_id) is set.
ALTER TABLE "alert_outbox" ALTER COLUMN "alert_id" DROP NOT NULL;
ALTER TABLE "alert_outbox" ADD COLUMN "notification_channel_id" TEXT;
ALTER TABLE "alert_outbox" ADD CONSTRAINT "alert_outbox_notification_channel_id_fkey"
    FOREIGN KEY ("notification_channel_id") REFERENCES "notification_channels"("id") ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE "alert_outbox" ADD CONSTRAINT "alert_outbox_source_check"
    CHECK (("alert_id" IS NOT NULL) <> ("notification_channel_id" IS NOT NULL));

-- alert_history is partitioned (see 20260627120000_partition_timeseries_tables);
-- ALTER TABLE on the parent propagates to all partitions.
ALTER TABLE "alert_history" ALTER COLUMN "alert_id" DROP NOT NULL;
ALTER TABLE "alert_history" ADD COLUMN "notification_channel_id" TEXT;
ALTER TABLE "alert_history" ADD CONSTRAINT "alert_history_notification_channel_id_fkey"
    FOREIGN KEY ("notification_channel_id") REFERENCES "notification_channels"("id") ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE "alert_history" ADD CONSTRAINT "alert_history_source_check"
    CHECK (("alert_id" IS NOT NULL) <> ("notification_channel_id" IS NOT NULL));
