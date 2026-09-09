-- SSL-certificate and domain expiry monitoring (ENTERPRISE, HTTP monitors
-- only). These are sub-checks of the existing monitor — not separate monitor
-- types — checked by a dedicated low-cadence sweep (internal/scheduler/
-- expiry.go, at most once per 24h per monitor+kind), not the per-interval
-- check loop: TLS handshakes are cheap but RDAP is rate-limited, and expiry
-- only moves daily.
--
-- Alerts bypass quorum/incidents entirely (days-to-expiry is threshold-based,
-- not up/down, and must not pollute uptime/MTBF stats): the sweep inserts
-- outbox rows directly via maintenance.enqueue_monitor_alerts, which brings
-- retries, alert_history, channel fan-out, and maintenance-window suppression
-- (checked by the caller) for free.
--
-- Re-alert policy: alert on the first crossing of each bucket — configured
-- threshold, 3 days, 1 day, expired (bucket 0) — tracked via last_alert_bucket
-- + alerted_expires_at. A renewal changes expires_at, which mismatches
-- alerted_expires_at and re-arms every bucket. Bucket math lives in Go
-- (models.ExpiryAlertBucket); the columns are just state.

ALTER TABLE "monitors" ADD COLUMN "cert_check_enabled"           BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE "monitors" ADD COLUMN "cert_expiry_threshold_days"   INTEGER NOT NULL DEFAULT 14;
ALTER TABLE "monitors" ADD COLUMN "domain_check_enabled"         BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE "monitors" ADD COLUMN "domain_expiry_threshold_days" INTEGER NOT NULL DEFAULT 14;

CREATE TABLE "monitor_expiry_status" (
    "monitor_id"         TEXT NOT NULL,
    "kind"               TEXT NOT NULL,
    "expires_at"         TIMESTAMPTZ,
    "checked_at"         TIMESTAMPTZ NOT NULL DEFAULT now(),
    "last_error"         TEXT,
    -- Days-bucket last alerted for the expires_at in alerted_expires_at
    -- (threshold / 3 / 1 / 0=expired); NULL = never alerted.
    "last_alert_bucket"  INTEGER,
    "alerted_expires_at" TIMESTAMPTZ,

    CONSTRAINT "monitor_expiry_status_pkey" PRIMARY KEY ("monitor_id", "kind"),
    CONSTRAINT "monitor_expiry_status_kind_check" CHECK ("kind" IN ('CERT', 'DOMAIN'))
);

ALTER TABLE "monitor_expiry_status" ADD CONSTRAINT "monitor_expiry_status_monitor_id_fkey"
    FOREIGN KEY ("monitor_id") REFERENCES "monitors"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- The sweep scans by staleness.
CREATE INDEX "monitor_expiry_status_checked_at_idx" ON "monitor_expiry_status"("checked_at");
