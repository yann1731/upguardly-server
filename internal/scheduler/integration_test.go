package scheduler

// Integration tests for the scheduler's database paths: the incident tracker,
// the batched result writer, and the alert outbox enqueue/claim round trip.
// They need a real Postgres with migrations applied and are skipped unless
// SCHEDULER_TEST_DATABASE_URL is set, e.g.:
//
//	docker run -d --name pgtest -e POSTGRES_PASSWORD=test -e POSTGRES_DB=upguardly -p 55433:5432 postgres:18-alpine
//	DATABASE_URL="postgresql://postgres:test@localhost:55433/upguardly?sslmode=disable" \
//	  go run github.com/steebchen/prisma-client-go migrate deploy
//	SCHEDULER_TEST_DATABASE_URL="postgresql://postgres:test@localhost:55433/upguardly?sslmode=disable" \
//	  go test ./internal/scheduler/ -v

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"upguardly-backend/internal/database"
	db "upguardly-backend/internal/database/prisma"
	"upguardly-backend/internal/models"
)

func integrationDB(t *testing.T) *database.Client {
	t.Helper()
	url := os.Getenv("SCHEDULER_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SCHEDULER_TEST_DATABASE_URL not set")
	}
	os.Setenv("DATABASE_URL", url)

	client := database.NewClient()
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect() })
	return client
}

func createTestMonitor(t *testing.T, dbc *database.Client, suffix string) *db.MonitorModel {
	t.Helper()
	ctx := context.Background()
	m, err := dbc.Prisma.Monitor.CreateOne(
		db.Monitor.UserID.Set("it-user"),
		db.Monitor.Name.Set("it-monitor-"+suffix),
		db.Monitor.Type.Set(db.MonitorTypeHTTP),
		db.Monitor.Target.Set("https://example.com"),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.Prisma.Monitor.FindUnique(db.Monitor.ID.Equals(m.ID)).Delete().Exec(ctx)
	})
	return m
}

func TestIncidentTrackerTransitions(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	m := createTestMonitor(t, dbc, fmt.Sprintf("inc-%d", time.Now().UnixNano()))

	tracker := newIncidentTracker(dbc)

	down := &models.CheckResult{Status: models.StatusDOWN, Message: "Server error"}
	degraded := &models.CheckResult{Status: models.StatusDEGRADED, Message: "High latency"}
	up := &models.CheckResult{Status: models.StatusUP, Message: "OK"}

	// First unhealthy check ever must open (and therefore alert) — this is
	// the restart-safety property the SQLite state store lacked.
	if got := tracker.record(ctx, m.ID, degraded); got != transitionOpened {
		t.Fatalf("first DEGRADED: got %v, want transitionOpened", got)
	}
	// Same severity again: no transition.
	if got := tracker.record(ctx, m.ID, degraded); got != transitionNone {
		t.Fatalf("repeat DEGRADED: got %v, want transitionNone", got)
	}
	// DEGRADED -> DOWN escalates.
	if got := tracker.record(ctx, m.ID, down); got != transitionEscalated {
		t.Fatalf("DEGRADED->DOWN: got %v, want transitionEscalated", got)
	}
	// DOWN -> DEGRADED is not a de-escalation alert (worst status is sticky).
	if got := tracker.record(ctx, m.ID, degraded); got != transitionNone {
		t.Fatalf("DOWN->DEGRADED: got %v, want transitionNone", got)
	}
	// Recovery resolves.
	if got := tracker.record(ctx, m.ID, up); got != transitionResolved {
		t.Fatalf("recovery: got %v, want transitionResolved", got)
	}
	// Healthy steady state: nothing.
	if got := tracker.record(ctx, m.ID, up); got != transitionNone {
		t.Fatalf("steady UP: got %v, want transitionNone", got)
	}

	// A fresh tracker (as after a restart or partition handoff) must see the
	// still-open incident from the DB, not re-open a second one.
	if got := tracker.record(ctx, m.ID, down); got != transitionOpened {
		t.Fatalf("re-open after resolve: got %v, want transitionOpened", got)
	}
	fresh := newIncidentTracker(dbc)
	if got := fresh.record(ctx, m.ID, down); got != transitionNone {
		t.Fatalf("fresh tracker on open incident: got %v, want transitionNone", got)
	}

	open, err := dbc.Prisma.Incident.FindMany(
		db.Incident.MonitorID.Equals(m.ID),
		db.Incident.ResolvedAt.IsNull(),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1", len(open))
	}
	if open[0].Status != db.StatusDown {
		t.Fatalf("incident status = %s, want DOWN (worst seen)", open[0].Status)
	}
}

func TestResultWriterBatchFlush(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	m := createTestMonitor(t, dbc, fmt.Sprintf("rw-%d", time.Now().UnixNano()))

	w := newResultWriter(dbc)
	code := 500
	for i := 0; i < 5; i++ {
		w.enqueue(ctx, m.ID, &models.CheckResult{
			Status:     models.StatusDOWN,
			Latency:    100 + i,
			Message:    "Server error",
			StatusCode: &code,
		})
	}
	w.stop() // drains and flushes

	rows, err := dbc.Prisma.MonitorResult.FindMany(
		db.MonitorResult.MonitorID.Equals(m.ID),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("results = %d, want 5", len(rows))
	}
	if sc, ok := rows[0].StatusCode(); !ok || sc != 500 {
		t.Fatalf("status code = %v/%v, want 500", sc, ok)
	}
}

// Covers the raw multi-row INSERT edges the happy path misses: a nil
// status code must land as NULL, a row whose monitor no longer exists
// must be skipped without failing the rest of the batch, and a message
// with SQL metacharacters must land verbatim (message text can come from
// the monitored target, so it is attacker-influenced).
func TestResultWriterFlushNullsAndDeletedMonitor(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	m := createTestMonitor(t, dbc, fmt.Sprintf("rwx-%d", time.Now().UnixNano()))

	hostile := `', 0, NULL, ''); DROP TABLE monitor_results; --`
	w := newResultWriter(dbc)
	defer w.stop()
	w.flush([]pendingResult{
		{monitorID: m.ID, result: models.CheckResult{Status: models.StatusUP, Latency: 12, Message: hostile}},
		{monitorID: "no-such-monitor", result: models.CheckResult{Status: models.StatusUP, Latency: 1}},
	})

	rows, err := dbc.Prisma.MonitorResult.FindMany(
		db.MonitorResult.MonitorID.Equals(m.ID),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("results = %d, want 1", len(rows))
	}
	if _, ok := rows[0].StatusCode(); ok {
		t.Fatalf("status code should be NULL")
	}
	if rows[0].Status != db.StatusUp {
		t.Fatalf("status = %s, want UP", rows[0].Status)
	}
	if msg, ok := rows[0].Message(); !ok || msg != hostile {
		t.Fatalf("message = %q/%v, want the raw payload stored verbatim", msg, ok)
	}
}

func TestOutboxEnqueueAndClaim(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()

	// A unique owner per run: channels are per-user, so reusing "it-user"
	// would leak channels between runs.
	owner := fmt.Sprintf("it-user-ob-%d", time.Now().UnixNano())
	m, err := dbc.Prisma.Monitor.CreateOne(
		db.Monitor.UserID.Set(owner),
		db.Monitor.Name.Set("it-monitor-ob"),
		db.Monitor.Type.Set(db.MonitorTypeHTTP),
		db.Monitor.Target.Set("https://example.com"),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.Prisma.Monitor.FindUnique(db.Monitor.ID.Equals(m.ID)).Delete().Exec(ctx)
	})

	alertCfg, err := dbc.Prisma.NotificationChannel.CreateOne(
		db.NotificationChannel.UserID.Set(owner),
		db.NotificationChannel.Channel.Set(db.AlertChannelEmail),
		db.NotificationChannel.Target.Set("it@example.com"),
		db.NotificationChannel.Enabled.Set(true),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.Prisma.NotificationChannel.FindUnique(db.NotificationChannel.ID.Equals(alertCfg.ID)).Delete().Exec(ctx)
	})

	// Enqueue through the runner path (without starting a dispatcher, so the
	// rows stay claimable by this test).
	r := &checkRunner{db: dbc}
	code := 503
	r.enqueueAlerts(ctx, m, &models.CheckResult{
		Status:     models.StatusDOWN,
		Latency:    42,
		Message:    "Server error",
		StatusCode: &code,
	})

	// Claim through the dispatcher's raw query: this exercises the
	// UPDATE ... RETURNING round trip and the raw-row JSON scanning.
	var rows []outboxRow
	if err := dbc.Prisma.Prisma.QueryRaw(claimQuery).Exec(ctx, &rows); err != nil {
		t.Fatalf("claim query: %v", err)
	}

	var row *outboxRow
	for i := range rows {
		if rows[i].NotificationChannelID != nil && string(*rows[i].NotificationChannelID) == alertCfg.ID {
			row = &rows[i]
		}
	}
	if row == nil {
		t.Fatalf("enqueued outbox row not claimed (got %d rows)", len(rows))
	}

	if string(row.MonitorID) != m.ID ||
		string(row.Channel) != "EMAIL" ||
		string(row.Target) != "it@example.com" ||
		string(row.Status) != "DOWN" ||
		string(row.MonitorName) != m.Name ||
		string(row.MonitorType) != "HTTP" ||
		int(row.Latency) != 42 ||
		int(row.Attempts) != 1 {
		t.Fatalf("claimed row fields wrong: %+v", row)
	}
	if row.StatusCode == nil || int(*row.StatusCode) != 503 {
		t.Fatalf("status code = %v, want 503", row.StatusCode)
	}

	// Claimed rows must not be claimable again until their backoff elapses.
	var again []outboxRow
	if err := dbc.Prisma.Prisma.QueryRaw(claimQuery).Exec(ctx, &again); err != nil {
		t.Fatalf("second claim: %v", err)
	}
	for i := range again {
		if string(again[i].ID) == string(row.ID) {
			t.Fatalf("row claimed twice within backoff window")
		}
	}

	// finalize must write history and remove the row.
	d := &alertDispatcher{db: dbc}
	d.finalize(ctx, row, "delivered")

	left, err := dbc.Prisma.AlertOutbox.FindMany(
		db.AlertOutbox.NotificationChannelID.Equals(alertCfg.ID),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("outbox rows left = %d, want 0", len(left))
	}

	hist, err := dbc.Prisma.AlertHistory.FindMany(
		db.AlertHistory.NotificationChannelID.Equals(alertCfg.ID),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("history rows = %d, want 1", len(hist))
	}
	if hist[0].Message != "delivered" {
		t.Fatalf("history message = %q, want %q", hist[0].Message, "delivered")
	}
}

// Covers the global notification-channel enqueue path: a monitor inherits
// the owner's channels, a per-monitor override opts one channel out, and
// finalize links history to the channel.
func TestOutboxGlobalChannels(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()

	// A unique owner per run: channels are per-user, so reusing "it-user"
	// would leak channels between runs.
	owner := fmt.Sprintf("it-user-gc-%d", time.Now().UnixNano())
	m, err := dbc.Prisma.Monitor.CreateOne(
		db.Monitor.UserID.Set(owner),
		db.Monitor.Name.Set("it-monitor-gc"),
		db.Monitor.Type.Set(db.MonitorTypeHTTP),
		db.Monitor.Target.Set("https://example.com"),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.Prisma.Monitor.FindUnique(db.Monitor.ID.Equals(m.ID)).Delete().Exec(ctx)
	})

	newChannel := func(channel db.AlertChannel, target string) *db.NotificationChannelModel {
		ch, err := dbc.Prisma.NotificationChannel.CreateOne(
			db.NotificationChannel.UserID.Set(owner),
			db.NotificationChannel.Channel.Set(channel),
			db.NotificationChannel.Target.Set(target),
			db.NotificationChannel.Enabled.Set(true),
		).Exec(ctx)
		if err != nil {
			t.Fatalf("create channel: %v", err)
		}
		t.Cleanup(func() {
			_, _ = dbc.Prisma.NotificationChannel.FindUnique(db.NotificationChannel.ID.Equals(ch.ID)).Delete().Exec(ctx)
		})
		return ch
	}

	chEmail := newChannel(db.AlertChannelEmail, "global@example.com")
	chDiscord := newChannel(db.AlertChannelDiscord, "https://discord.example.com/webhook")

	// Opt the monitor out of the Discord channel.
	if _, err := dbc.Prisma.MonitorChannelSetting.CreateOne(
		db.MonitorChannelSetting.Monitor.Link(db.Monitor.ID.Equals(m.ID)),
		db.MonitorChannelSetting.NotificationChannel.Link(db.NotificationChannel.ID.Equals(chDiscord.ID)),
		db.MonitorChannelSetting.Enabled.Set(false),
	).Exec(ctx); err != nil {
		t.Fatalf("create channel setting: %v", err)
	}

	r := &checkRunner{db: dbc}
	result := &models.CheckResult{Status: models.StatusDOWN, Latency: 7, Message: "down"}
	r.enqueueAlerts(ctx, m, result)

	rows, err := dbc.Prisma.AlertOutbox.FindMany(
		db.AlertOutbox.MonitorID.Equals(m.ID),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("outbox rows = %d, want 1 (email inherited, discord opted out)", len(rows))
	}
	if chID, ok := rows[0].NotificationChannelID(); !ok || chID != chEmail.ID {
		t.Fatalf("row channel link = %v/%v, want %s", chID, ok, chEmail.ID)
	}
	if _, ok := rows[0].AlertID(); ok {
		t.Fatalf("global-channel row must have NULL alert_id")
	}

	// finalize on a global-channel row links history to the channel.
	var claimed []outboxRow
	if err := dbc.Prisma.Prisma.QueryRaw(claimQuery).Exec(ctx, &claimed); err != nil {
		t.Fatalf("claim: %v", err)
	}
	var row *outboxRow
	for i := range claimed {
		if claimed[i].NotificationChannelID != nil && string(*claimed[i].NotificationChannelID) == chEmail.ID {
			row = &claimed[i]
		}
	}
	if row == nil {
		t.Fatalf("global-channel outbox row not claimed")
	}
	d := &alertDispatcher{db: dbc}
	d.finalize(ctx, row, "delivered")

	hist, err := dbc.Prisma.AlertHistory.FindMany(
		db.AlertHistory.NotificationChannelID.Equals(chEmail.ID),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("history rows = %d, want 1", len(hist))
	}
	if _, ok := hist[0].AlertID(); ok {
		t.Fatalf("global-channel history must have NULL alert_id")
	}
}
