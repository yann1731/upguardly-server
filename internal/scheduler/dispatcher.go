package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/database"
	db "upguardly-backend/internal/database/prisma"
	"upguardly-backend/internal/metrics"
	"upguardly-backend/internal/models"
)

const (
	dispatchInterval    = 5 * time.Second
	dispatchBatchSize   = 50
	dispatchMaxAttempts = 8
	dispatchSendTimeout = 30 * time.Second
)

// alertDispatcher drains the alert_outbox table: it claims due rows with
// FOR UPDATE SKIP LOCKED (so any number of scheduler instances can run a
// dispatcher concurrently without double-sending), attempts delivery, deletes
// the row on success, and otherwise leaves it to retry with exponential
// backoff. A provider outage therefore delays alerts instead of losing them.
type alertDispatcher struct {
	db           *database.Client
	alertManager *alerter.Manager
	stopCh       chan struct{}
	doneCh       chan struct{}
	once         sync.Once
}

func newAlertDispatcher(dbc *database.Client, alertManager *alerter.Manager) *alertDispatcher {
	d := &alertDispatcher{
		db:           dbc,
		alertManager: alertManager,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
	go d.loop()
	return d
}

func (d *alertDispatcher) stop() {
	d.once.Do(func() { close(d.stopCh) })
	select {
	case <-d.doneCh:
	case <-time.After(10 * time.Second):
		log.Printf("Timeout waiting for alert dispatcher to stop")
	}
}

func (d *alertDispatcher) loop() {
	defer close(d.doneCh)

	ticker := time.NewTicker(dispatchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			// A full batch means there may be a backlog: keep draining
			// until a batch comes back short.
			for d.dispatchBatch() == dispatchBatchSize {
				select {
				case <-d.stopCh:
					return
				default:
				}
			}
		}
	}
}

// outboxRow mirrors the RETURNING clause of the claim query. Raw scalar types
// unmarshal the query-engine's JSON encoding.
type outboxRow struct {
	ID            db.RawString `json:"id"`
	AlertID       db.RawString `json:"alertId"`
	MonitorID     db.RawString `json:"monitorId"`
	Channel       db.RawString `json:"channel"`
	Target        db.RawString `json:"target"`
	Status        db.RawString `json:"status"`
	Message       db.RawString `json:"message"`
	StatusCode    *db.RawInt   `json:"statusCode"`
	Latency       db.RawInt    `json:"latency"`
	MonitorName   db.RawString `json:"monitorName"`
	MonitorType   db.RawString `json:"monitorType"`
	MonitorTarget db.RawString `json:"monitorTarget"`
	Attempts      db.RawInt    `json:"attempts"`
}

// claimQuery atomically claims up to dispatchBatchSize due rows: attempts is
// bumped and next_attempt_at pushed out (30s * 2^attempts, capped at 30m)
// BEFORE delivery is attempted, so rows claimed by a dispatcher that crashes
// mid-send retry automatically after the backoff. SKIP LOCKED keeps
// concurrent dispatchers from claiming the same rows.
var claimQuery = fmt.Sprintf(`
UPDATE alert_outbox
SET attempts = attempts + 1,
    next_attempt_at = now() + least(interval '30 seconds' * (2 ^ attempts), interval '30 minutes')
WHERE id IN (
    SELECT id FROM alert_outbox
    WHERE next_attempt_at <= now()
    ORDER BY next_attempt_at
    LIMIT %d
    FOR UPDATE SKIP LOCKED
)
RETURNING id, alert_id AS "alertId", monitor_id AS "monitorId", channel, target,
          status, message, status_code AS "statusCode", latency,
          monitor_name AS "monitorName", monitor_type AS "monitorType",
          monitor_target AS "monitorTarget", attempts`, dispatchBatchSize)

// dispatchBatch claims and processes one batch, returning how many rows it
// claimed.
func (d *alertDispatcher) dispatchBatch() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var rows []outboxRow
	if err := d.db.Prisma.Prisma.QueryRaw(claimQuery).Exec(ctx, &rows); err != nil {
		log.Printf("Failed to claim alert outbox rows: %v", err)
		return 0
	}

	for i := range rows {
		d.deliver(ctx, &rows[i])
	}

	return len(rows)
}

func (d *alertDispatcher) deliver(ctx context.Context, row *outboxRow) {
	mon := &models.Monitor{
		ID:     string(row.MonitorID),
		Name:   string(row.MonitorName),
		Type:   models.MonitorType(row.MonitorType),
		Target: string(row.MonitorTarget),
	}
	result := &models.CheckResult{
		Status:  models.Status(row.Status),
		Latency: int(row.Latency),
		Message: string(row.Message),
	}
	if row.StatusCode != nil {
		code := int(*row.StatusCode)
		result.StatusCode = &code
	}

	sendCtx, cancel := context.WithTimeout(ctx, dispatchSendTimeout)
	err := d.alertManager.Send(sendCtx, models.AlertChannel(row.Channel), string(row.Target), mon, result)
	cancel()

	switch {
	case err == nil:
		log.Printf("Sent %s alert for %s", row.Channel, mon.Name)
		metrics.AlertsSentTotal.WithLabelValues(mon.ID, mon.Name, string(row.Channel), string(result.Status)).Inc()
		d.finalize(ctx, row, result.Message)

	case int(row.Attempts) >= dispatchMaxAttempts:
		log.Printf("Giving up on %s alert for %s after %d attempts: %v", row.Channel, mon.Name, row.Attempts, err)
		d.finalize(ctx, row, fmt.Sprintf("delivery failed after %d attempts: %v", row.Attempts, err))

	default:
		// Leave the row in place; the backoff set at claim time schedules
		// the retry.
		log.Printf("Failed to send %s alert for %s (attempt %d, will retry): %v", row.Channel, mon.Name, row.Attempts, err)
	}
}

// finalize records the outcome in alert_history and removes the outbox row.
func (d *alertDispatcher) finalize(ctx context.Context, row *outboxRow, historyMessage string) {
	if _, err := d.db.Prisma.AlertHistory.CreateOne(
		db.AlertHistory.Alert.Link(db.Alert.ID.Equals(string(row.AlertID))),
		db.AlertHistory.Status.Set(db.Status(row.Status)),
		db.AlertHistory.Message.Set(historyMessage),
	).Exec(ctx); err != nil {
		log.Printf("Failed to save alert history: %v", err)
	}

	if _, err := d.db.Prisma.AlertOutbox.FindUnique(
		db.AlertOutbox.ID.Equals(string(row.ID)),
	).Delete().Exec(ctx); err != nil {
		log.Printf("Failed to delete outbox row %s: %v", row.ID, err)
	}
}
