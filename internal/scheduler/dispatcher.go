package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/metrics"
	"upguardly-backend/internal/models"
)

const (
	dispatchInterval    = 5 * time.Second
	dispatchBatchSize   = 50
	dispatchMaxAttempts = 8
	dispatchSendTimeout = 30 * time.Second
	// dispatchConcurrency bounds parallel sends within a claimed batch.
	// Sequential delivery caps throughput at ~1/provider-latency (a few
	// alerts/sec) — during a mass outage a backlog of thousands would take
	// tens of minutes to drain. Alerters are stateless and safe for
	// concurrent use (see alerter.Alerter); the bound keeps us polite to
	// provider rate limits.
	dispatchConcurrency = 8
)

// alertDispatcher drains the alert_outbox table: it claims due rows with
// FOR UPDATE SKIP LOCKED (so any number of scheduler instances can run a
// dispatcher concurrently without double-sending), attempts delivery, deletes
// the row on success, and otherwise leaves it to retry with exponential
// backoff. A provider outage therefore delays alerts instead of losing them.
type alertDispatcher struct {
	store        models.SchedulerStore
	alertManager *alerter.Manager
	stopCh       chan struct{}
	doneCh       chan struct{}
	once         sync.Once
}

func newAlertDispatcher(store models.SchedulerStore, alertManager *alerter.Manager) *alertDispatcher {
	d := &alertDispatcher{
		store:        store,
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

// dispatchBatch claims and processes one batch, returning how many rows it
// claimed.
func (d *alertDispatcher) dispatchBatch() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rows, err := d.store.ClaimOutboxAlerts(ctx, dispatchBatchSize)
	if err != nil {
		log.Printf("Failed to claim alert outbox rows: %v", err)
		return 0
	}

	// Rows are claimed exclusively by this dispatcher (SKIP LOCKED), so they
	// can be delivered in parallel without coordination.
	sem := make(chan struct{}, dispatchConcurrency)
	var wg sync.WaitGroup
	for i := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(row *models.AlertOutboxRow) {
			defer wg.Done()
			defer func() { <-sem }()
			d.deliver(ctx, row)
		}(&rows[i])
	}
	wg.Wait()

	return len(rows)
}

func (d *alertDispatcher) deliver(ctx context.Context, row *models.AlertOutboxRow) {
	mon := &models.Monitor{
		ID:     row.MonitorID,
		Name:   row.MonitorName,
		Type:   row.MonitorType,
		Target: row.MonitorTarget,
	}
	result := &models.CheckResult{
		Status:     row.Status,
		Latency:    row.Latency,
		StatusCode: row.StatusCode,
		Message:    row.Message,
	}

	sendCtx, cancel := context.WithTimeout(ctx, dispatchSendTimeout)
	err := d.alertManager.Send(sendCtx, row.Channel, row.Target, mon, result)
	cancel()

	switch {
	case err == nil:
		log.Printf("Sent %s alert for %s", row.Channel, mon.Name)
		metrics.AlertsSentTotal.WithLabelValues(mon.ID, mon.Name, string(row.Channel), string(result.Status)).Inc()
		d.finalize(ctx, row, result.Message)

	case row.Attempts >= dispatchMaxAttempts:
		log.Printf("Giving up on %s alert for %s after %d attempts: %v", row.Channel, mon.Name, row.Attempts, err)
		d.finalize(ctx, row, fmt.Sprintf("delivery failed after %d attempts: %v", row.Attempts, err))

	default:
		// Leave the row in place; the backoff set at claim time schedules
		// the retry.
		log.Printf("Failed to send %s alert for %s (attempt %d, will retry): %v", row.Channel, mon.Name, row.Attempts, err)
	}
}

// finalize records the outcome in alert_history and removes the outbox row.
func (d *alertDispatcher) finalize(ctx context.Context, row *models.AlertOutboxRow, historyMessage string) {
	err := d.store.FinalizeOutboxAlert(ctx, row.ID, row.Status, historyMessage, row.AlertID, row.NotificationChannelID)
	if err != nil {
		log.Printf("Failed to finalize outbox row %s: %v", row.ID, err)
	}
}
