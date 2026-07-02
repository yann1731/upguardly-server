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

// enqueueAlerts writes one outbox row per effective alert config instead of
// sending inline. The dispatcher delivers with retries, so a slow or down
// provider neither blocks the check loop nor loses the alert.
//
// The effective channels for a monitor are the owner's global notification
// channels, where a per-monitor MonitorChannelSetting overrides the channel's
// global enabled flag (absent row = inherit).
func (r *checkRunner) enqueueAlerts(ctx context.Context, m *db.MonitorModel, result *models.CheckResult) {
	channels := r.effectiveGlobalChannels(ctx, m)

	queries := make([]transaction.Transaction, 0, len(channels))
	for _, ch := range channels {
		queries = append(queries, r.outboxCreate(m, result, ch.Channel, ch.Target,
			db.AlertOutbox.NotificationChannel.Link(db.NotificationChannel.ID.Equals(ch.ID))).Tx())
	}

	if len(queries) == 0 {
		return
	}

	if err := r.db.Prisma.Prisma.Transaction(queries...).Exec(ctx); err != nil {
		log.Printf("Failed to enqueue %d alert(s) for %s: %v", len(queries), m.Name, err)
	}
}

// outboxCreate builds one outbox insert; source links the row to the global
// NotificationChannel it came from.
func (r *checkRunner) outboxCreate(m *db.MonitorModel, result *models.CheckResult, channel db.AlertChannel, target string, source db.AlertOutboxSetParam) alertOutboxInsert {
	optionalParams := []db.AlertOutboxSetParam{
		source,
		db.AlertOutbox.Latency.Set(result.Latency),
	}
	if result.StatusCode != nil {
		optionalParams = append(optionalParams, db.AlertOutbox.StatusCode.Set(*result.StatusCode))
	}

	return r.db.Prisma.AlertOutbox.CreateOne(
		db.AlertOutbox.MonitorID.Set(m.ID),
		db.AlertOutbox.Channel.Set(channel),
		db.AlertOutbox.Target.Set(target),
		db.AlertOutbox.Status.Set(db.Status(result.Status)),
		db.AlertOutbox.Message.Set(result.Message),
		db.AlertOutbox.MonitorName.Set(m.Name),
		db.AlertOutbox.MonitorType.Set(m.Type),
		db.AlertOutbox.MonitorTarget.Set(m.Target),
		optionalParams...,
	)
}

// effectiveGlobalChannels returns the owner's global channels that apply to
// this monitor after per-monitor overrides. Channels are per-user; an org
// monitor uses the org owner's channels, mirroring how an org's effective
// plan is its owner's plan.
func (r *checkRunner) effectiveGlobalChannels(ctx context.Context, m *db.MonitorModel) []db.NotificationChannelModel {
	ownerID := m.UserID
	if orgID, ok := m.OrgID(); ok {
		org, err := r.db.Prisma.Organization.FindUnique(
			db.Organization.ID.Equals(orgID),
		).Exec(ctx)
		if err != nil {
			log.Printf("Failed to resolve org %s owner for monitor %s, using monitor creator: %v", orgID, m.ID, err)
		} else {
			ownerID = org.OwnerID
		}
	}

	// Fetch all of the owner's channels (not just globally enabled ones): a
	// per-monitor override can opt in to a channel that is off globally.
	channels, err := r.db.Prisma.NotificationChannel.FindMany(
		db.NotificationChannel.UserID.Equals(ownerID),
	).Exec(ctx)
	if err != nil {
		log.Printf("Failed to fetch notification channels for %s: %v", ownerID, err)
		return nil
	}
	if len(channels) == 0 {
		return nil
	}

	settings, err := r.db.Prisma.MonitorChannelSetting.FindMany(
		db.MonitorChannelSetting.MonitorID.Equals(m.ID),
	).Exec(ctx)
	if err != nil {
		log.Printf("Failed to fetch channel settings for monitor %s: %v", m.ID, err)
		return nil
	}
	overrides := make(map[string]bool, len(settings))
	for _, s := range settings {
		overrides[s.NotificationChannelID] = s.Enabled
	}

	effective := channels[:0]
	for _, ch := range channels {
		enabled := ch.Enabled
		if o, ok := overrides[ch.ID]; ok {
			enabled = o
		}
		if enabled {
			effective = append(effective, ch)
		}
	}
	return effective
}

// alertOutboxInsert is the subset of the generated create builder
// enqueueAlerts needs (the concrete builder type is unexported).
type alertOutboxInsert interface {
	Tx() db.AlertOutboxUniqueTxResult
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
	sb.WriteString(`INSERT INTO monitor_results (id, monitor_id, status, latency, status_code, message) ` +
		`SELECT v.id, v.monitor_id, v.status::"Status", v.latency, v.status_code, v.message FROM (VALUES `)
	params := make([]interface{}, 0, len(batch)*6)
	for i, p := range batch {
		if i > 0 {
			sb.WriteString(", ")
		}
		n := len(params)
		fmt.Fprintf(&sb, `($%d::text, $%d::text, $%d::text, $%d::int4, $%d::int4, $%d::text)`,
			n+1, n+2, n+3, n+4, n+5, n+6)
		var statusCode interface{}
		if p.result.StatusCode != nil {
			statusCode = *p.result.StatusCode
		}
		params = append(params,
			uuid.NewString(), p.monitorID, string(p.result.Status),
			p.result.Latency, statusCode, p.result.Message,
		)
	}
	sb.WriteString(`) AS v(id, monitor_id, status, latency, status_code, message) ` +
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
