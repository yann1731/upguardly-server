package scheduler

// Integration tests for the scheduler's database paths: the quorum function
// (maintenance.record_region_check) driven through the runner's Go call path,
// the batched result writer, and the alert outbox claim/finalize round trip.
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
	"strings"
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

func createTestMonitor(t *testing.T, dbc *database.Client, suffix string, regions ...string) *db.MonitorModel {
	t.Helper()
	ctx := context.Background()
	if len(regions) == 0 {
		regions = []string{"na-east"}
	}
	m, err := dbc.Prisma.Monitor.CreateOne(
		db.Monitor.UserID.Set("it-user"),
		db.Monitor.Name.Set("it-monitor-"+suffix),
		db.Monitor.Type.Set(db.MonitorTypeHTTP),
		db.Monitor.Target.Set("https://example.com"),
		db.Monitor.Regions.Set(regions),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.Prisma.Monitor.FindUnique(db.Monitor.ID.Equals(m.ID)).Delete().Exec(ctx)
	})
	return m
}

// record drives the production recordRegionCheck path for one region.
func record(t *testing.T, dbc *database.Client, m *db.MonitorModel, region string, result *models.CheckResult) string {
	t.Helper()
	store := database.NewPrismaStore(dbc)
	r := &checkRunner{store: store, region: region}
	regions := make([]string, len(m.Regions))
	for i, r := range m.Regions {
		regions[i] = string(r)
	}
	mon := &models.Monitor{
		ID:        m.ID,
		Name:      m.Name,
		Type:      models.MonitorType(m.Type),
		Target:    m.Target,
		Interval:  m.Interval,
		Timeout:   m.Timeout,
		Enabled:   m.Enabled,
		Regions:   regions,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
	return r.recordRegionCheck(context.Background(), mon, result)
}

func openIncidents(t *testing.T, dbc *database.Client, monitorID string) []db.IncidentModel {
	t.Helper()
	open, err := dbc.Prisma.Incident.FindMany(
		db.Incident.MonitorID.Equals(monitorID),
		db.Incident.ResolvedAt.IsNull(),
	).Exec(context.Background())
	if err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	return open
}

// Single-region monitors must keep the exact transition semantics the old
// in-memory incidentTracker had: open on first unhealthy check ever (the
// restart-safety property), escalate on DEGRADED->DOWN, sticky worst status,
// resolve on recovery, and never a duplicate open incident — even from a
// "fresh" runner, since all state now lives in Postgres.
func TestRegionCheckSingleRegionTransitions(t *testing.T) {
	dbc := integrationDB(t)
	m := createTestMonitor(t, dbc, fmt.Sprintf("inc-%d", time.Now().UnixNano()), "na-east")

	down := &models.CheckResult{Status: models.StatusDOWN, Message: "Server error"}
	degraded := &models.CheckResult{Status: models.StatusDEGRADED, Message: "High latency"}
	up := &models.CheckResult{Status: models.StatusUP, Message: "OK"}

	if got := record(t, dbc, m, "na-east", degraded); got != "opened" {
		t.Fatalf("first DEGRADED: got %q, want opened", got)
	}
	if got := record(t, dbc, m, "na-east", degraded); got != "none" {
		t.Fatalf("repeat DEGRADED: got %q, want none", got)
	}
	if got := record(t, dbc, m, "na-east", down); got != "escalated" {
		t.Fatalf("DEGRADED->DOWN: got %q, want escalated", got)
	}
	if got := record(t, dbc, m, "na-east", degraded); got != "none" {
		t.Fatalf("DOWN->DEGRADED (sticky worst): got %q, want none", got)
	}
	if got := record(t, dbc, m, "na-east", up); got != "resolved" {
		t.Fatalf("recovery: got %q, want resolved", got)
	}
	if got := record(t, dbc, m, "na-east", up); got != "none" {
		t.Fatalf("steady UP: got %q, want none", got)
	}
	if got := record(t, dbc, m, "na-east", down); got != "opened" {
		t.Fatalf("re-open after resolve: got %q, want opened", got)
	}
	// Any other writer (fresh instance, handoff duplicate) sees the open
	// incident in the DB and must not open a second one.
	if got := record(t, dbc, m, "na-east", down); got != "none" {
		t.Fatalf("second writer on open incident: got %q, want none", got)
	}

	open := openIncidents(t, dbc, m.ID)
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1", len(open))
	}
	if open[0].Status != db.StatusDown {
		t.Fatalf("incident status = %s, want DOWN (worst seen)", open[0].Status)
	}
}

// Majority quorum across three regions: one region down is a minority (no
// incident), a second makes it a majority (opened, message names both), and
// one recovery drops it back below quorum (resolved).
func TestRegionCheckQuorum(t *testing.T) {
	dbc := integrationDB(t)
	m := createTestMonitor(t, dbc, fmt.Sprintf("q-%d", time.Now().UnixNano()),
		"na-east", "eu-west", "ap-southeast")

	down := &models.CheckResult{Status: models.StatusDOWN, Message: "refused"}
	up := &models.CheckResult{Status: models.StatusUP, Message: "ok"}

	if got := record(t, dbc, m, "eu-west", down); got != "none" {
		t.Fatalf("1/3 down: got %q, want none", got)
	}
	if n := len(openIncidents(t, dbc, m.ID)); n != 0 {
		t.Fatalf("open incidents after minority = %d, want 0", n)
	}

	if got := record(t, dbc, m, "na-east", down); got != "opened" {
		t.Fatalf("2/3 down: got %q, want opened", got)
	}
	open := openIncidents(t, dbc, m.ID)
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1", len(open))
	}
	msg, _ := open[0].Message()
	if !strings.Contains(msg, "eu-west") || !strings.Contains(msg, "na-east") || !strings.Contains(msg, "2/3") {
		t.Fatalf("incident message %q should name both failing regions and the 2/3 count", msg)
	}

	// The healthy third region reporting again must not resolve anything.
	if got := record(t, dbc, m, "ap-southeast", up); got != "none" {
		t.Fatalf("healthy minority report: got %q, want none", got)
	}

	if got := record(t, dbc, m, "eu-west", up); got != "resolved" {
		t.Fatalf("recovery to 1/3: got %q, want resolved", got)
	}
	if n := len(openIncidents(t, dbc, m.ID)); n != 0 {
		t.Fatalf("open incidents after resolve = %d, want 0", n)
	}
}

// A region whose last report is older than the staleness window (3x interval)
// must stop counting toward quorum: a dead region pool can neither hold an
// incident open nor open one.
func TestRegionCheckStaleRegionIgnored(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	m := createTestMonitor(t, dbc, fmt.Sprintf("st-%d", time.Now().UnixNano()),
		"na-east", "eu-west")

	down := &models.CheckResult{Status: models.StatusDOWN, Message: "refused"}
	up := &models.CheckResult{Status: models.StatusUP, Message: "ok"}

	record(t, dbc, m, "eu-west", down)
	if got := record(t, dbc, m, "na-east", down); got != "opened" {
		t.Fatalf("2/2 down: got %q, want opened", got)
	}

	// eu-west's pool dies: its DOWN row goes stale (interval is 60s, so 1h is
	// far past the 180s freshness window).
	if _, err := dbc.Prisma.Prisma.ExecuteRaw(
		`UPDATE monitor_region_status SET checked_at = now() - interval '1 hour'
		  WHERE monitor_id = $1 AND region = 'eu-west'`, m.ID,
	).Exec(ctx); err != nil {
		t.Fatalf("backdate region status: %v", err)
	}

	// With eu-west stale, na-east UP means 0 fresh unhealthy regions -> resolve.
	if got := record(t, dbc, m, "na-east", up); got != "resolved" {
		t.Fatalf("recovery with stale region: got %q, want resolved", got)
	}
	// And a lone na-east DOWN afterwards is 1/2 — not a majority.
	if got := record(t, dbc, m, "na-east", down); got != "none" {
		t.Fatalf("1/2 down with stale region: got %q, want none", got)
	}
}

// Alert enqueue is now inside the quorum function's transaction. Covers: one
// outbox row per effective channel on open, a per-monitor override opting a
// channel out, the recovery row on resolve, org monitors resolving channels
// via the org owner, and the dispatcher's claim/finalize round trip against
// function-generated rows.
func TestRegionCheckOutboxAtomic(t *testing.T) {
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
		db.Monitor.Regions.Set([]string{"na-east"}),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.Prisma.Monitor.FindUnique(db.Monitor.ID.Equals(m.ID)).Delete().Exec(ctx)
	})

	newChannel := func(user string, channel db.AlertChannel, target string) *db.NotificationChannelModel {
		ch, err := dbc.Prisma.NotificationChannel.CreateOne(
			db.NotificationChannel.UserID.Set(user),
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

	chEmail := newChannel(owner, db.AlertChannelEmail, "it@example.com")
	chDiscord := newChannel(owner, db.AlertChannelDiscord, "https://discord.example.com/webhook")

	// Opt the monitor out of the Discord channel.
	if _, err := dbc.Prisma.MonitorChannelSetting.CreateOne(
		db.MonitorChannelSetting.Monitor.Link(db.Monitor.ID.Equals(m.ID)),
		db.MonitorChannelSetting.NotificationChannel.Link(db.NotificationChannel.ID.Equals(chDiscord.ID)),
		db.MonitorChannelSetting.Enabled.Set(false),
	).Exec(ctx); err != nil {
		t.Fatalf("create channel setting: %v", err)
	}

	code := 503
	if got := record(t, dbc, m, "na-east", &models.CheckResult{
		Status: models.StatusDOWN, Latency: 42, Message: "Server error", StatusCode: &code,
	}); got != "opened" {
		t.Fatalf("open: got %q, want opened", got)
	}

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
	if rows[0].Target != "it@example.com" || rows[0].Status != db.StatusDown ||
		rows[0].MonitorName != m.Name || rows[0].MonitorType != db.MonitorTypeHTTP ||
		rows[0].MonitorTarget != m.Target || rows[0].Latency != 42 {
		t.Fatalf("outbox denormalized fields wrong: %+v", rows[0])
	}
	if sc, ok := rows[0].StatusCode(); !ok || sc != 503 {
		t.Fatalf("status code = %v/%v, want 503", sc, ok)
	}

	// Claim through the dispatcher's raw query: exercises the
	// UPDATE ... RETURNING round trip against a function-generated row.
	store := database.NewPrismaStore(dbc)
	claimed, err := store.ClaimOutboxAlerts(ctx, dispatchBatchSize)
	if err != nil {
		t.Fatalf("claim query: %v", err)
	}
	var row *models.AlertOutboxRow
	for i := range claimed {
		if claimed[i].NotificationChannelID != nil && *claimed[i].NotificationChannelID == chEmail.ID {
			row = &claimed[i]
		}
	}
	if row == nil {
		t.Fatalf("enqueued outbox row not claimed (got %d rows)", len(claimed))
	}
	if row.Attempts != 1 {
		t.Fatalf("claimed attempts = %d, want 1", row.Attempts)
	}

	// Claimed rows must not be claimable again until their backoff elapses.
	again, err := store.ClaimOutboxAlerts(ctx, dispatchBatchSize)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	for i := range again {
		if again[i].ID == row.ID {
			t.Fatalf("row claimed twice within backoff window")
		}
	}

	// finalize must write history and remove the row.
	d := &alertDispatcher{store: store}
	d.finalize(ctx, row, "delivered")

	left, err := dbc.Prisma.AlertOutbox.FindMany(
		db.AlertOutbox.NotificationChannelID.Equals(chEmail.ID),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("outbox rows left = %d, want 0", len(left))
	}
	hist, err := dbc.Prisma.AlertHistory.FindMany(
		db.AlertHistory.NotificationChannelID.Equals(chEmail.ID),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(hist) != 1 || hist[0].Message != "delivered" {
		t.Fatalf("history = %+v, want one 'delivered' row", hist)
	}

	// Recovery enqueues an UP row with the recovering check's message.
	if got := record(t, dbc, m, "na-east", &models.CheckResult{
		Status: models.StatusUP, Latency: 10, Message: "OK",
	}); got != "resolved" {
		t.Fatalf("resolve: got %q, want resolved", got)
	}
	upRows, err := dbc.Prisma.AlertOutbox.FindMany(
		db.AlertOutbox.MonitorID.Equals(m.ID),
		db.AlertOutbox.Status.Equals(db.StatusUp),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("list recovery outbox: %v", err)
	}
	if len(upRows) != 1 || upRows[0].Message != "OK" {
		t.Fatalf("recovery rows = %+v, want one UP row with message OK", upRows)
	}

	// Org monitor: channels resolve via the org owner, not the creator.
	orgOwner := owner + "-orgowner"
	chOrg := newChannel(orgOwner, db.AlertChannelSlack, "https://slack.example.com/hook")
	org, err := dbc.Prisma.Organization.CreateOne(
		db.Organization.Name.Set(fmt.Sprintf("it-org-%d", time.Now().UnixNano())),
		db.Organization.OwnerID.Set(orgOwner),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.Prisma.Organization.FindUnique(db.Organization.ID.Equals(org.ID)).Delete().Exec(ctx)
	})
	orgMon, err := dbc.Prisma.Monitor.CreateOne(
		db.Monitor.UserID.Set(owner+"-creator"),
		db.Monitor.Name.Set("it-monitor-org"),
		db.Monitor.Type.Set(db.MonitorTypeHTTP),
		db.Monitor.Target.Set("https://example.com"),
		db.Monitor.Regions.Set([]string{"na-east"}),
		db.Monitor.Org.Link(db.Organization.ID.Equals(org.ID)),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("create org monitor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.Prisma.Monitor.FindUnique(db.Monitor.ID.Equals(orgMon.ID)).Delete().Exec(ctx)
	})

	if got := record(t, dbc, orgMon, "na-east", &models.CheckResult{
		Status: models.StatusDOWN, Message: "down",
	}); got != "opened" {
		t.Fatalf("org open: got %q, want opened", got)
	}
	orgRows, err := dbc.Prisma.AlertOutbox.FindMany(
		db.AlertOutbox.MonitorID.Equals(orgMon.ID),
	).Exec(ctx)
	if err != nil {
		t.Fatalf("list org outbox: %v", err)
	}
	if len(orgRows) != 1 {
		t.Fatalf("org outbox rows = %d, want 1", len(orgRows))
	}
	if chID, ok := orgRows[0].NotificationChannelID(); !ok || chID != chOrg.ID {
		t.Fatalf("org row channel = %v/%v, want org owner's channel %s", chID, ok, chOrg.ID)
	}
}

func TestResultWriterBatchFlush(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	m := createTestMonitor(t, dbc, fmt.Sprintf("rw-%d", time.Now().UnixNano()))

	w := newResultWriter(database.NewPrismaStore(dbc), "eu-west")
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
	for _, row := range rows {
		if row.Region != "eu-west" {
			t.Fatalf("region = %q, want eu-west", row.Region)
		}
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
	w := newResultWriter(database.NewPrismaStore(dbc), "na-east")
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
	if rows[0].Region != "na-east" {
		t.Fatalf("region = %q, want na-east", rows[0].Region)
	}
}
