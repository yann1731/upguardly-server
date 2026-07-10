-- CreateTable
CREATE TABLE "alert_outbox" (
    "id" TEXT NOT NULL,
    "alert_id" TEXT NOT NULL,
    "monitor_id" TEXT NOT NULL,
    "channel" "AlertChannel" NOT NULL,
    "target" TEXT NOT NULL,
    "status" "Status" NOT NULL,
    "message" TEXT NOT NULL,
    "status_code" INTEGER,
    "latency" INTEGER NOT NULL DEFAULT 0,
    "monitor_name" TEXT NOT NULL,
    "monitor_type" "MonitorType" NOT NULL,
    "monitor_target" TEXT NOT NULL,
    "attempts" INTEGER NOT NULL DEFAULT 0,
    "next_attempt_at" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "created_at" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "alert_outbox_pkey" PRIMARY KEY ("id")
);

-- CreateIndex: the dispatcher's claim query scans by due time.
CREATE INDEX "alert_outbox_next_attempt_at_idx" ON "alert_outbox"("next_attempt_at");

-- AddForeignKey
ALTER TABLE "alert_outbox" ADD CONSTRAINT "alert_outbox_alert_id_fkey" FOREIGN KEY ("alert_id") REFERENCES "alerts"("id") ON DELETE CASCADE ON UPDATE CASCADE;
