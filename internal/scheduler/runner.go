package scheduler

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

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
	region     string
	results    *resultWriter
	dispatcher *alertDispatcher
}

func newCheckRunner(dbc *database.Client, alertManager *alerter.Manager, region string) *checkRunner {
	return &checkRunner{
		db:         dbc,
		region:     region,
		results:    newResultWriter(dbc, region),
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

func (r *checkRunner) runCheck(ctx context.Context, m *db.MonitorModel) {
	checker := monitor.NewChecker(models.MonitorType(m.Type))
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

// regionCheckRow is the result of maintenance.record_region_check.
type regionCheckRow struct {
	Transition db.RawString  `json:"transition"`
	IncidentID *db.RawString `json:"incidentId"`
}

// recordRegionCheck reports this region's check outcome to Postgres and
// returns the incident transition it caused ("none", "opened", "escalated",
// "resolved"). Everything stateful lives in maintenance.record_region_check
// (migration 20260703120000_add_regions): it upserts this region's row,
// evaluates majority quorum across the monitor's configured regions under a
// per-monitor advisory lock, transitions the global incident, and enqueues
// alert_outbox rows in the same transaction. This replaced the in-memory
// incidentTracker and Go-side enqueueAlerts/effectiveGlobalChannels: with
// multiple regions checking the same monitor there is no single writer any
// more, so the serialization has to live in the database.
//
// On error we only log: the next check retries, which is the same recovery
// semantics the tracker had (a DB error meant "stay unloaded; retry").
func (r *checkRunner) recordRegionCheck(ctx context.Context, m *db.MonitorModel, result *models.CheckResult) string {
	var statusCode interface{}
	if result.StatusCode != nil {
		statusCode = *result.StatusCode
	}

	var rows []regionCheckRow
	err := r.db.Prisma.Prisma.QueryRaw(
		`SELECT transition, incident_id AS "incidentId"
		   FROM maintenance.record_region_check($1::text, $2::text, $3::"Status", $4::int4, $5::int4, $6::text)`,
		m.ID, r.region, string(result.Status), result.Latency, statusCode, result.Message,
	).Exec(ctx, &rows)
	if err != nil {
		log.Printf("Failed to record region check for %s: %v", m.ID, err)
		return "none"
	}
	if len(rows) == 0 {
		return "none"
	}
	return string(rows[0].Transition)
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
	db     *database.Client
	region string
	ch     chan pendingResult
	done   chan struct{}
	once   sync.Once
}

func newResultWriter(dbc *database.Client, region string) *resultWriter {
	w := &resultWriter{
		db:     dbc,
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

// flush writes the batch as one multi-row INSERT via ExecuteRaw. The
// generated CreateOne path costs a full query-engine round trip (protocol
// parse, plan, relation connect) per row, and that engine work — not
// Postgres — dominates backend CPU at high check volume. IDs are generated
// here because the cuid default lives in the Prisma client, not the DB.
// The join against monitors drops rows whose monitor was deleted between
// check and flush, which would otherwise fail the whole statement on FK.
func (w *resultWriter) flush(batch []pendingResult) {
	if len(batch) == 0 {
		return
	}

	// Deliberately not tied to any job context: results for canceled jobs
	// still get persisted on shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var sb strings.Builder
	sb.WriteString(`INSERT INTO monitor_results (id, monitor_id, status, latency, status_code, message, region) ` +
		`SELECT v.id, v.monitor_id, v.status::"Status", v.latency, v.status_code, v.message, v.region FROM (VALUES `)
	params := make([]interface{}, 0, len(batch)*7)
	for i, p := range batch {
		if i > 0 {
			sb.WriteString(", ")
		}
		n := len(params)
		fmt.Fprintf(&sb, `($%d::text, $%d::text, $%d::text, $%d::int4, $%d::int4, $%d::text, $%d::text)`,
			n+1, n+2, n+3, n+4, n+5, n+6, n+7)
		var statusCode interface{}
		if p.result.StatusCode != nil {
			statusCode = *p.result.StatusCode
		}
		params = append(params,
			uuid.NewString(), p.monitorID, string(p.result.Status),
			p.result.Latency, statusCode, p.result.Message, w.region,
		)
	}
	sb.WriteString(`) AS v(id, monitor_id, status, latency, status_code, message, region) ` +
		`JOIN monitors ON monitors.id = v.monitor_id`)

	res, err := w.db.Prisma.Prisma.ExecuteRaw(sb.String(), params...).Exec(ctx)
	if err != nil {
		log.Printf("Batch result insert failed (%d rows), retrying individually: %v", len(batch), err)
		for _, p := range batch {
			w.insertOne(ctx, p)
		}
		return
	}
	if res.Count < len(batch) {
		log.Printf("Batch result insert skipped %d of %d rows (monitor deleted)", len(batch)-res.Count, len(batch))
	}
}

// resultInsert is the subset of the generated create builder the writer
// needs (the concrete builder type is unexported).
type resultInsert interface {
	Exec(ctx context.Context) (*db.MonitorResultModel, error)
}

func (w *resultWriter) createQuery(p pendingResult) resultInsert {
	optionalParams := []db.MonitorResultSetParam{
		db.MonitorResult.Message.Set(p.result.Message),
		db.MonitorResult.Region.Set(w.region),
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
