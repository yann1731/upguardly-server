package scheduler

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/steebchen/prisma-client-go/runtime/transaction"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/database"
	db "upguardly-backend/internal/database/prisma"
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
	db         *database.Client
	results    *resultWriter
	dispatcher *alertDispatcher
}

func newCheckRunner(dbc *database.Client, alertManager *alerter.Manager) *checkRunner {
	return &checkRunner{
		db:         dbc,
		results:    newResultWriter(dbc),
		dispatcher: newAlertDispatcher(dbc, alertManager),
	}
}

// stop drains and stops the result writer and the alert dispatcher. Call
// after all jobs are canceled. Any alerts still queued in the outbox are
// picked up by another instance's dispatcher, or by this one on restart.
func (r *checkRunner) stop() {
	r.results.stop()
	r.dispatcher.stop()
}

// jobLoop checks m on its interval until ctx is canceled. The first check
// runs immediately; the loop then re-phases by a one-time random offset so
// jobs started in the same sync tick (e.g. all of them, after a restart)
// don't hit their targets and the database in lockstep forever.
func (r *checkRunner) jobLoop(ctx context.Context, m *db.MonitorModel) {
	tracker := newIncidentTracker(r.db)

	r.runCheck(ctx, m, tracker)

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
			r.runCheck(ctx, m, tracker)
		}
	}
}

func (r *checkRunner) runCheck(ctx context.Context, m *db.MonitorModel, tracker *incidentTracker) {
	checker := monitor.NewChecker(models.MonitorType(m.Type))
	if checker == nil {
		log.Printf("Unknown monitor type: %s", m.Type)
		return
	}

	timeout := time.Duration(m.Timeout) * time.Second
	result := checker.Check(ctx, m.Target, timeout)

	metrics.MonitorChecksTotal.WithLabelValues(m.ID, m.Name, string(m.Type), string(result.Status)).Inc()
	metrics.MonitorCheckLatencyMs.WithLabelValues(m.ID, m.Name, string(m.Type), string(result.Status)).Observe(float64(result.Latency))
	metrics.MonitorStatus.WithLabelValues(m.ID, m.Name, string(m.Type)).Set(metrics.StatusToGaugeValue(result.Status))

	r.results.enqueue(ctx, m.ID, &result)

	// Alerts fire on incident transitions. The open-incident row in Postgres
	// is the durable status memory shared by all instances, so alerting
	// survives restarts and partition handoffs, and a monitor that is
	// unhealthy from its very first check still alerts.
	if tracker.record(ctx, m.ID, &result) != transitionNone {
		r.enqueueAlerts(ctx, m, &result)
	}

	log.Printf("Monitor %s: %s (latency: %dms)", m.Name, result.Status, result.Latency)
}

// enqueueAlerts writes one outbox row per enabled alert config instead of
// sending inline. The dispatcher delivers with retries, so a slow or down
// provider neither blocks the check loop nor loses the alert.
func (r *checkRunner) enqueueAlerts(ctx context.Context, m *db.MonitorModel, result *models.CheckResult) {
	alerts, err := r.db.Prisma.Alert.FindMany(
		db.Alert.MonitorID.Equals(m.ID),
		db.Alert.Enabled.Equals(true),
	).Exec(ctx)

	if err != nil {
		log.Printf("Failed to fetch alerts: %v", err)
		return
	}
	if len(alerts) == 0 {
		return
	}

	queries := make([]transaction.Transaction, 0, len(alerts))
	for _, alert := range alerts {
		optionalParams := []db.AlertOutboxSetParam{
			db.AlertOutbox.Latency.Set(result.Latency),
		}
		if result.StatusCode != nil {
			optionalParams = append(optionalParams, db.AlertOutbox.StatusCode.Set(*result.StatusCode))
		}

		queries = append(queries, r.db.Prisma.AlertOutbox.CreateOne(
			db.AlertOutbox.Alert.Link(db.Alert.ID.Equals(alert.ID)),
			db.AlertOutbox.MonitorID.Set(m.ID),
			db.AlertOutbox.Channel.Set(alert.Channel),
			db.AlertOutbox.Target.Set(alert.Target),
			db.AlertOutbox.Status.Set(db.Status(result.Status)),
			db.AlertOutbox.Message.Set(result.Message),
			db.AlertOutbox.MonitorName.Set(m.Name),
			db.AlertOutbox.MonitorType.Set(m.Type),
			db.AlertOutbox.MonitorTarget.Set(m.Target),
			optionalParams...,
		).Tx())
	}

	if err := r.db.Prisma.Prisma.Transaction(queries...).Exec(ctx); err != nil {
		log.Printf("Failed to enqueue %d alert(s) for %s: %v", len(queries), m.Name, err)
	}
}

const (
	// resultBatchMax bounds how many results one flush writes; resultFlushEvery
	// bounds how stale a buffered result can get. Together they turn up to
	// resultBatchMax single-row round trips per second into one batched
	// transaction.
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
// flush interval instead of one round trip per check.
type resultWriter struct {
	db   *database.Client
	ch   chan pendingResult
	done chan struct{}
	once sync.Once
}

func newResultWriter(dbc *database.Client) *resultWriter {
	w := &resultWriter{
		db:   dbc,
		ch:   make(chan pendingResult, resultBufferSize),
		done: make(chan struct{}),
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

	queries := make([]transaction.Transaction, 0, len(batch))
	for _, p := range batch {
		queries = append(queries, w.createQuery(p).Tx())
	}

	if err := w.db.Prisma.Prisma.Transaction(queries...).Exec(ctx); err != nil {
		// The whole transaction fails if any single row does (e.g. its
		// monitor was deleted between check and flush). Retry rows
		// individually so one bad row can't discard the batch.
		log.Printf("Batch result insert failed (%d rows), retrying individually: %v", len(batch), err)
		for _, p := range batch {
			w.insertOne(ctx, p)
		}
	}
}

// resultInsert is the subset of the generated create builder the writer
// needs (the concrete builder type is unexported).
type resultInsert interface {
	Tx() db.MonitorResultUniqueTxResult
	Exec(ctx context.Context) (*db.MonitorResultModel, error)
}

func (w *resultWriter) createQuery(p pendingResult) resultInsert {
	optionalParams := []db.MonitorResultSetParam{
		db.MonitorResult.Message.Set(p.result.Message),
	}
	if p.result.StatusCode != nil {
		optionalParams = append(optionalParams, db.MonitorResult.StatusCode.Set(*p.result.StatusCode))
	}

	return w.db.Prisma.MonitorResult.CreateOne(
		db.MonitorResult.Monitor.Link(db.Monitor.ID.Equals(p.monitorID)),
		db.MonitorResult.Status.Set(db.Status(p.result.Status)),
		db.MonitorResult.Latency.Set(p.result.Latency),
		optionalParams...,
	)
}

func (w *resultWriter) insertOne(ctx context.Context, p pendingResult) {
	if _, err := w.createQuery(p).Exec(ctx); err != nil {
		log.Printf("Failed to save monitor result for %s: %v", p.monitorID, err)
	}
}
