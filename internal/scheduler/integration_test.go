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

	"github.com/google/uuid"
	bundb "upguardly-backend/internal/database/bun"
	"upguardly-backend/internal/models"
)

func integrationDB(t *testing.T) *bundb.Client {
	t.Helper()
	url := os.Getenv("SCHEDULER_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SCHEDULER_TEST_DATABASE_URL not set")
	}
	os.Setenv("DATABASE_URL", url)

	client := bundb.NewClient(url)
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect() })
	return client
}

func createTestMonitor(t *testing.T, dbc *bundb.Client, suffix string, regions ...string) *models.Monitor {
	t.Helper()
	ctx := context.Background()
	if len(regions) == 0 {
		regions = []string{"na-east"}
	}
	m := &bundb.Monitor{
		ID:        uuid.NewString(),
		UserID:    "it-user",
		Name:      "it-monitor-" + suffix,
		Type:      string(models.MonitorTypeHTTP),
		Target:    "https://example.com",
		Interval:  60,
		Timeout:   30,
		Enabled:   true,
		Regions:   regions,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, err := dbc.DB.NewInsert().Model(m).Exec(ctx); err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.DB.NewDelete().Table("monitors").Where("id = ?", m.ID).Exec(ctx)
	})

	return &models.Monitor{
		ID:        m.ID,
		OrgID:     m.OrgID,
		Name:      m.Name,
		Type:      models.MonitorType(m.Type),
		Target:    m.Target,
		Interval:  m.Interval,
		Timeout:   m.Timeout,
		Enabled:   m.Enabled,
		Regions:   m.Regions,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// record drives the production recordRegionCheck path for one region.
func record(t *testing.T, dbc *bundb.Client, m *models.Monitor, region string, result *models.CheckResult) string {
	t.Helper()
	store := bundb.NewBunStore(dbc)
	r := &checkRunner{store: store, region: region}
	return r.recordRegionCheck(context.Background(), m, result)
}

func openIncidents(t *testing.T, dbc *bundb.Client, monitorID string) []bundb.Incident {
	t.Helper()
	var open []bundb.Incident
	err := dbc.DB.NewSelect().
		Model(&open).
		Where("monitor_id = ?", monitorID).
		Where("resolved_at IS NULL").
		Scan(context.Background())
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
	if open[0].Status != string(models.StatusDOWN) {
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
	msg := open[0].Message
	if msg == nil || !strings.Contains(*msg, "eu-west") || !strings.Contains(*msg, "na-east") || !strings.Contains(*msg, "2/3") {
		t.Fatalf("incident message %v should name both failing regions and the 2/3 count", msg)
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
	if _, err := dbc.DB.NewRaw(
		`UPDATE monitor_region_status SET checked_at = now() - interval '1 hour'
		  WHERE monitor_id = ? AND region = 'eu-west'`, m.ID,
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
	m := &bundb.Monitor{
		ID:        uuid.NewString(),
		UserID:    owner,
		Name:      "it-monitor-ob",
		Type:      string(models.MonitorTypeHTTP),
		Target:    "https://example.com",
		Interval:  60,
		Timeout:   30,
		Enabled:   true,
		Regions:   []string{"na-east"},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, err := dbc.DB.NewInsert().Model(m).Exec(ctx); err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.DB.NewDelete().Table("monitors").Where("id = ?", m.ID).Exec(ctx)
	})

	newChannel := func(user string, channel string, target string) *bundb.NotificationChannel {
		ch := &bundb.NotificationChannel{
			ID:        uuid.NewString(),
			UserID:    user,
			Channel:   channel,
			Target:    target,
			Enabled:   true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if _, err := dbc.DB.NewInsert().Model(ch).Exec(ctx); err != nil {
			t.Fatalf("create channel: %v", err)
		}
		t.Cleanup(func() {
			_, _ = dbc.DB.NewDelete().Table("notification_channels").Where("id = ?", ch.ID).Exec(ctx)
		})
		return ch
	}

	chEmail := newChannel(owner, string(models.AlertChannelEMAIL), "it@example.com")
	chDiscord := newChannel(owner, string(models.AlertChannelDISCORD), "https://discord.example.com/webhook")

	// Opt the monitor out of the Discord channel.
	setting := &bundb.MonitorChannelSetting{
		ID:                    uuid.NewString(),
		MonitorID:             m.ID,
		NotificationChannelID: chDiscord.ID,
		Enabled:               false,
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}
	if _, err := dbc.DB.NewInsert().Model(setting).Exec(ctx); err != nil {
		t.Fatalf("create channel setting: %v", err)
	}

	code := 503
	monModel := &models.Monitor{
		ID:        m.ID,
		OrgID:     m.OrgID,
		Name:      m.Name,
		Type:      models.MonitorType(m.Type),
		Target:    m.Target,
		Interval:  m.Interval,
		Timeout:   m.Timeout,
		Enabled:   m.Enabled,
		Regions:   m.Regions,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
	if got := record(t, dbc, monModel, "na-east", &models.CheckResult{
		Status: models.StatusDOWN, Latency: 42, Message: "Server error", StatusCode: &code,
	}); got != "opened" {
		t.Fatalf("open: got %q, want opened", got)
	}

	var rows []bundb.AlertOutbox
	err := dbc.DB.NewSelect().
		Model(&rows).
		Where("monitor_id = ?", m.ID).
		Scan(ctx)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("outbox rows = %d, want 1 (email inherited, discord opted out)", len(rows))
	}
	if rows[0].NotificationChannelID == nil || *rows[0].NotificationChannelID != chEmail.ID {
		t.Fatalf("row channel link = %v, want %s", rows[0].NotificationChannelID, chEmail.ID)
	}
	if rows[0].AlertID != nil {
		t.Fatalf("global-channel row must have NULL alert_id")
	}
	if rows[0].Target != "it@example.com" || rows[0].Status != string(models.StatusDOWN) ||
		rows[0].MonitorName != m.Name || rows[0].MonitorType != m.Type ||
		rows[0].MonitorTarget != m.Target || rows[0].Latency != 42 {
		t.Fatalf("outbox denormalized fields wrong: %+v", rows[0])
	}
	if rows[0].StatusCode == nil || *rows[0].StatusCode != 503 {
		t.Fatalf("status code = %v, want 503", rows[0].StatusCode)
	}

	// Claim through the dispatcher's raw query: exercises the
	// UPDATE ... RETURNING round trip against a function-generated row.
	store := bundb.NewBunStore(dbc)
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

	var left []bundb.AlertOutbox
	err = dbc.DB.NewSelect().
		Model(&left).
		Where("notification_channel_id = ?", chEmail.ID).
		Scan(ctx)
	if err != nil {
		t.Fatalf("list outbox: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("outbox rows left = %d, want 0", len(left))
	}

	var hist []bundb.AlertHistory
	err = dbc.DB.NewSelect().
		Model(&hist).
		Where("notification_channel_id = ?", chEmail.ID).
		Scan(ctx)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(hist) != 1 || hist[0].Message != "delivered" {
		t.Fatalf("history = %+v, want one 'delivered' row", hist)
	}

	// Recovery enqueues an UP row with the recovering check's message.
	if got := record(t, dbc, monModel, "na-east", &models.CheckResult{
		Status: models.StatusUP, Latency: 10, Message: "OK",
	}); got != "resolved" {
		t.Fatalf("resolve: got %q, want resolved", got)
	}
	var upRows []bundb.AlertOutbox
	err = dbc.DB.NewSelect().
		Model(&upRows).
		Where("monitor_id = ?", m.ID).
		Where("status = ?", string(models.StatusUP)).
		Scan(ctx)
	if err != nil {
		t.Fatalf("list recovery outbox: %v", err)
	}
	if len(upRows) != 1 || upRows[0].Message != "OK" {
		t.Fatalf("recovery rows = %+v, want one UP row with message OK", upRows)
	}

	// Org monitor: channels resolve via the org owner, not the creator.
	orgOwner := owner + "-orgowner"
	chOrg := newChannel(orgOwner, string(models.AlertChannelSLACK), "https://slack.example.com/hook")
	org := &bundb.Organization{
		ID:        uuid.NewString(),
		Name:      fmt.Sprintf("it-org-%d", time.Now().UnixNano()),
		OwnerID:   orgOwner,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, err := dbc.DB.NewInsert().Model(org).Exec(ctx); err != nil {
		t.Fatalf("create org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.DB.NewDelete().Table("organizations").Where("id = ?", org.ID).Exec(ctx)
	})
	orgMon := &bundb.Monitor{
		ID:        uuid.NewString(),
		UserID:    owner + "-creator",
		Name:      "it-monitor-org",
		Type:      string(models.MonitorTypeHTTP),
		Target:    "https://example.com",
		Regions:   []string{"na-east"},
		OrgID:     &org.ID,
		Interval:  60,
		Timeout:   30,
		Enabled:   true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, err := dbc.DB.NewInsert().Model(orgMon).Exec(ctx); err != nil {
		t.Fatalf("create org monitor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.DB.NewDelete().Table("monitors").Where("id = ?", orgMon.ID).Exec(ctx)
	})

	orgMonModel := &models.Monitor{
		ID:        orgMon.ID,
		OrgID:     orgMon.OrgID,
		Name:      orgMon.Name,
		Type:      models.MonitorType(orgMon.Type),
		Target:    orgMon.Target,
		Interval:  orgMon.Interval,
		Timeout:   orgMon.Timeout,
		Enabled:   orgMon.Enabled,
		Regions:   orgMon.Regions,
		CreatedAt: orgMon.CreatedAt,
		UpdatedAt: orgMon.UpdatedAt,
	}

	if got := record(t, dbc, orgMonModel, "na-east", &models.CheckResult{
		Status: models.StatusDOWN, Message: "down",
	}); got != "opened" {
		t.Fatalf("org open: got %q, want opened", got)
	}
	var orgRows []bundb.AlertOutbox
	err = dbc.DB.NewSelect().
		Model(&orgRows).
		Where("monitor_id = ?", orgMon.ID).
		Scan(ctx)
	if err != nil {
		t.Fatalf("list org outbox: %v", err)
	}
	if len(orgRows) != 1 {
		t.Fatalf("org outbox rows = %d, want 1", len(orgRows))
	}
	if orgRows[0].NotificationChannelID == nil || *orgRows[0].NotificationChannelID != chOrg.ID {
		t.Fatalf("org row channel = %v, want org owner's channel %s", orgRows[0].NotificationChannelID, chOrg.ID)
	}
}

func TestResultWriterBatchFlush(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	m := createTestMonitor(t, dbc, fmt.Sprintf("rw-%d", time.Now().UnixNano()))

	w := newResultWriter(bundb.NewBunStore(dbc), "eu-west")
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

	var rows []bundb.MonitorResult
	err := dbc.DB.NewSelect().
		Model(&rows).
		Where("monitor_id = ?", m.ID).
		Scan(ctx)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("results = %d, want 5", len(rows))
	}
	if rows[0].StatusCode == nil || *rows[0].StatusCode != 500 {
		t.Fatalf("status code = %v, want 500", rows[0].StatusCode)
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
	w := newResultWriter(bundb.NewBunStore(dbc), "na-east")
	defer w.stop()
	w.flush([]pendingResult{
		{monitorID: m.ID, result: models.CheckResult{Status: models.StatusUP, Latency: 12, Message: hostile}},
		{monitorID: "no-such-monitor", result: models.CheckResult{Status: models.StatusUP, Latency: 1}},
	})

	var rows []bundb.MonitorResult
	err := dbc.DB.NewSelect().
		Model(&rows).
		Where("monitor_id = ?", m.ID).
		Scan(ctx)
	if err != nil {
		t.Fatalf("list results: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("results = %d, want 1", len(rows))
	}
	if rows[0].StatusCode != nil {
		t.Fatalf("status code should be NULL")
	}
	if rows[0].Status != string(models.StatusUP) {
		t.Fatalf("status = %s, want UP", rows[0].Status)
	}
	if rows[0].Message == nil || *rows[0].Message != hostile {
		t.Fatalf("message = %v, want the raw payload stored verbatim", rows[0].Message)
	}
	if rows[0].Region != "na-east" {
		t.Fatalf("region = %q, want na-east", rows[0].Region)
	}
}
