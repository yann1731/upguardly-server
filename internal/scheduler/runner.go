package scheduler

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/metrics"
	"upguardly-backend/internal/models"
	"upguardly-backend/internal/monitor"
)

// checkRunner holds the check/alert logic shared by the embedded and
// distributed schedulers. Each monitor job is one goroutine running jobLoop
// with an immutable config snapshot; the scheduler restarts the job when the
// monitor's updatedAt changes, so the loop never has to re-read config from
// the database.
type checkRunner struct {
	store      models.SchedulerStore
	region     string
	results    *resultWriter
	dispatcher *alertDispatcher
	verifier   *verificationWorker
}

func newCheckRunner(store models.SchedulerStore, alertManager *alerter.Manager, region string) *checkRunner {
	return &checkRunner{
		store:      store,
		region:     region,
		results:    newResultWriter(store, region),
		dispatcher: newAlertDispatcher(store, alertManager),
		verifier:   newVerificationWorker(store, region),
	}
}

// stop drains and stops the result writer, the alert dispatcher, and the
// verification worker. Call after all jobs are canceled. Any alerts still
// queued in the outbox are picked up by another instance's dispatcher, or by
// this one on restart.
func (r *checkRunner) stop() {
	r.results.stop()
	r.dispatcher.stop()
	r.verifier.stop()
}

// jobLoop checks m on its interval until ctx is canceled. The first check
// runs immediately; the loop then re-phases by a one-time random offset so
// jobs started in the same sync tick (e.g. all of them, after a restart)
// don't hit their targets and the database in lockstep forever.
func (r *checkRunner) jobLoop(ctx context.Context, m *models.Monitor) {
	r.runCheck(ctx, m)

	interval := time.Duration(m.Interval) * time.Second
	if interval <= 0 {
		log.Printf("Monitor %s has invalid interval %d, not scheduling", m.ID, m.Interval)
		return
	}

	jitter := time.Duration(rand.Int63n(int64(interval)))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runCheck(ctx, m)
		}
	}
}

func (r *checkRunner) runCheck(ctx context.Context, m *models.Monitor) {
	checker := monitor.NewChecker(m.Type)
	if checker == nil {
		log.Printf("Unknown monitor type: %s", m.Type)
		return
	}

	timeout := time.Duration(m.Timeout) * time.Second
	result := checker.Check(ctx, m.Target, timeout)

	metrics.MonitorChecksTotal.WithLabelValues(m.ID, m.Name, string(m.Type), string(result.Status), r.region).Inc()
	metrics.MonitorCheckLatencyMs.WithLabelValues(m.ID, m.Name, string(m.Type), string(result.Status), r.region).Observe(float64(result.Latency))
	metrics.MonitorStatus.WithLabelValues(m.ID, m.Name, string(m.Type), r.region).Set(metrics.StatusToGaugeValue(result.Status))

	r.results.enqueue(ctx, m.ID, &result)

	if transition := r.recordRegionCheck(ctx, m, &result); transition != "none" {
		log.Printf("Monitor %s: incident %s (region %s reported %s)", m.Name, transition, r.region, result.Status)
	}

	log.Printf("Monitor %s [%s]: %s (latency: %dms)", m.Name, r.region, result.Status, result.Latency)
}

// recordRegionCheck reports this region's check outcome to Postgres and
// returns the incident transition it caused ("none", "opened", "escalated",
// "resolved"). Everything stateful lives in maintenance.record_region_check
// (migration 20260703120000_add_regions): it upserts this region's row,
// evaluates majority quorum across the monitor's configured regions under a
// per-monitor advisory lock, transitions the global incident, and enqueues
// alert_outbox rows in the same transaction.
func (r *checkRunner) recordRegionCheck(ctx context.Context, m *models.Monitor, result *models.CheckResult) string {
	transition, err := r.store.RecordRegionCheck(ctx, m.ID, r.region, result, models.SourceScheduled)
	if err != nil {
		log.Printf("Failed to record region check for %s: %v", m.ID, err)
		return "none"
	}
	return transition
}

const (
	// resultBatchMax bounds how many results one flush writes; resultFlushEvery
	// bounds how stale a buffered result can get. Together they turn up to
	// resultBatchMax single-row round trips per second into one multi-row
	// INSERT.
	resultBatchMax   = 200
	resultFlushEvery = time.Second
	resultBufferSize = 4096
)

type pendingResult struct {
	monitorID string
	result    models.CheckResult
}

// resultWriter batches monitor_results inserts. Check goroutines enqueue and
// move on; a single flusher goroutine writes batches in one transaction per
// flush interval instead of one round trip per check. Every row is tagged
// with the region this scheduler runs in.
type resultWriter struct {
	store  models.SchedulerStore
	region string
	ch     chan pendingResult
	done   chan struct{}
	once   sync.Once
}

func newResultWriter(store models.SchedulerStore, region string) *resultWriter {
	w := &resultWriter{
		store:  store,
		region: region,
		ch:     make(chan pendingResult, resultBufferSize),
		done:   make(chan struct{}),
	}
	go w.loop()
	return w
}

// enqueue hands a result to the flusher. If the buffer is full (flusher
// stalled or DB down), it falls back to a synchronous single insert rather
// than silently dropping monitoring data.
func (w *resultWriter) enqueue(ctx context.Context, monitorID string, result *models.CheckResult) {
	select {
	case w.ch <- pendingResult{monitorID: monitorID, result: *result}:
	default:
		log.Printf("Result buffer full, writing result for %s synchronously", monitorID)
		w.insertOne(ctx, pendingResult{monitorID: monitorID, result: *result})
	}
}

// stop drains outstanding results and stops the flusher.
func (w *resultWriter) stop() {
	w.once.Do(func() { close(w.ch) })
	select {
	case <-w.done:
	case <-time.After(10 * time.Second):
		log.Printf("Timeout waiting for result writer to drain")
	}
}

func (w *resultWriter) loop() {
	defer close(w.done)

	ticker := time.NewTicker(resultFlushEvery)
	defer ticker.Stop()

	batch := make([]pendingResult, 0, resultBatchMax)

	for {
		select {
		case p, ok := <-w.ch:
			if !ok {
				w.flush(batch)
				return
			}
			batch = append(batch, p)
			if len(batch) >= resultBatchMax {
				w.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				w.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

func (w *resultWriter) flush(batch []pendingResult) {
	if len(batch) == 0 {
		return
	}

	// Deliberately not tied to any job context: results for canceled jobs
	// still get persisted on shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results := make([]models.PendingResult, len(batch))
	for i, b := range batch {
		results[i] = models.PendingResult{
			MonitorID: b.monitorID,
			Result:    b.result,
		}
	}

	if err := w.store.PersistMonitorResults(ctx, w.region, results); err != nil {
		log.Printf("Failed to persist monitor results (%d rows), error: %v", len(batch), err)
	}
}

func (w *resultWriter) insertOne(ctx context.Context, p pendingResult) {
	results := []models.PendingResult{
		{
			MonitorID: p.monitorID,
			Result:    p.result,
		},
	}
	if err := w.store.PersistMonitorResults(ctx, w.region, results); err != nil {
		log.Printf("Failed to save monitor result for %s: %v", p.monitorID, err)
	}
}
