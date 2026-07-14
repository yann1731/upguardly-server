package scheduler

// Integration tests for the scheduler's database paths: cross-region quorum and
// confirmation (maintenance.record_region_check / evaluate_monitor_quorum)
// driven through the runner's Go call path, follow-plan interval resolution, the
// batched result writer, and the alert outbox claim/finalize round trip. They
// need a real Postgres with migrations applied and are skipped unless
// SCHEDULER_TEST_DATABASE_URL is set, e.g.:
//
//	docker run -d --name pgtest -e POSTGRES_PASSWORD=test -e POSTGRES_DB=upguardly -p 55433:5432 postgres:18-alpine
//	DATABASE_URL="postgresql://postgres:test@localhost:55433/upguardly?sslmode=disable" \
//	  go run ./cmd/migrate
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
		regions = []string{"ca-east"}
	}
	m := &bundb.Monitor{
		ID:        uuid.NewString(),
		UserID:    "it-user",
		Name:      "it-monitor-" + suffix,
		Type:      string(models.MonitorTypeHTTP),
		Target:    "https://example.com",
		Interval:  ptrInt(60),
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
		Interval:  models.EffectiveInterval(m.Interval, "FREE", m.Timeout),
		Timeout:   m.Timeout,
		Enabled:   m.Enabled,
		Regions:   m.Regions,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func ptrInt(i int) *int { return &i }

// seedHeartbeats resets the region heartbeat table to exactly the given regions
// (marked live now) and clears it again on cleanup. Alert quorum counts a
// region only while its heartbeat is recent, and heartbeats are global (not
// per-monitor), so tests must establish the active set deterministically or
// state leaks between them.
func seedHeartbeats(t *testing.T, dbc *bundb.Client, regions ...string) {
	t.Helper()
	ctx := context.Background()
	store := bundb.NewBunStore(dbc)
	if _, err := dbc.DB.NewRaw(`DELETE FROM scheduler_region_heartbeats`).Exec(ctx); err != nil {
		t.Fatalf("clear heartbeats: %v", err)
	}
	for _, r := range regions {
		if err := store.UpsertRegionHeartbeat(ctx, r); err != nil {
			t.Fatalf("seed heartbeat %s: %v", r, err)
		}
	}
	t.Cleanup(func() {
		_, _ = dbc.DB.NewRaw(`DELETE FROM scheduler_region_heartbeats`).Exec(ctx)
	})
}

// record drives the production recordRegionCheck path for one region (a normal
// scheduled check).
func record(t *testing.T, dbc *bundb.Client, m *models.Monitor, region string, result *models.CheckResult) string {
	t.Helper()
	store := bundb.NewBunStore(dbc)
	r := &checkRunner{store: store, region: region}
	return r.recordRegionCheck(context.Background(), m, result)
}

// recordVerify drives a one-off confirmation check (SourceVerification), the
// path the verification worker takes for a region that doesn't normally check
// the monitor.
func recordVerify(t *testing.T, dbc *bundb.Client, monitorID, region string, result *models.CheckResult) string {
	t.Helper()
	got, err := bundb.NewBunStore(dbc).RecordRegionCheck(context.Background(), monitorID, region, result, models.SourceVerification)
	if err != nil {
		t.Fatalf("record verify: %v", err)
	}
	return got
}

// pendingVerifications returns the regions with an open confirmation request
// for the monitor, sorted.
func pendingVerifications(t *testing.T, dbc *bundb.Client, monitorID string) []string {
	t.Helper()
	var regions []string
	if err := dbc.DB.NewRaw(
		`SELECT region FROM region_verification_requests WHERE monitor_id = ? ORDER BY region`, monitorID,
	).Scan(context.Background(), &regions); err != nil {
		t.Fatalf("list verifications: %v", err)
	}
	return regions
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
	m := createTestMonitor(t, dbc, fmt.Sprintf("inc-%d", time.Now().UnixNano()), "ca-east")
	// Single-region deployment: only ca-east is active, so there is nothing to
	// confirm with — the monitor alerts on its own region, as before.
	seedHeartbeats(t, dbc, "ca-east")

	down := &models.CheckResult{Status: models.StatusDOWN, Message: "Server error"}
	degraded := &models.CheckResult{Status: models.StatusDEGRADED, Message: "High latency"}
	up := &models.CheckResult{Status: models.StatusUP, Message: "OK"}

	if got := record(t, dbc, m, "ca-east", degraded); got != "opened" {
		t.Fatalf("first DEGRADED: got %q, want opened", got)
	}
	if got := record(t, dbc, m, "ca-east", degraded); got != "none" {
		t.Fatalf("repeat DEGRADED: got %q, want none", got)
	}
	if got := record(t, dbc, m, "ca-east", down); got != "escalated" {
		t.Fatalf("DEGRADED->DOWN: got %q, want escalated", got)
	}
	if got := record(t, dbc, m, "ca-east", degraded); got != "none" {
		t.Fatalf("DOWN->DEGRADED (sticky worst): got %q, want none", got)
	}
	if got := record(t, dbc, m, "ca-east", up); got != "resolved" {
		t.Fatalf("recovery: got %q, want resolved", got)
	}
	if got := record(t, dbc, m, "ca-east", up); got != "none" {
		t.Fatalf("steady UP: got %q, want none", got)
	}
	if got := record(t, dbc, m, "ca-east", down); got != "opened" {
		t.Fatalf("re-open after resolve: got %q, want opened", got)
	}
	// Any other writer (fresh instance, handoff duplicate) sees the open
	// incident in the DB and must not open a second one.
	if got := record(t, dbc, m, "ca-east", down); got != "none" {
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
		"ca-east", "eu-west-fr", "eu-west-de")
	seedHeartbeats(t, dbc, "ca-east", "eu-west-fr", "eu-west-de")

	down := &models.CheckResult{Status: models.StatusDOWN, Message: "refused"}
	up := &models.CheckResult{Status: models.StatusUP, Message: "ok"}

	if got := record(t, dbc, m, "eu-west-fr", down); got != "none" {
		t.Fatalf("1/3 down: got %q, want none", got)
	}
	if n := len(openIncidents(t, dbc, m.ID)); n != 0 {
		t.Fatalf("open incidents after minority = %d, want 0", n)
	}

	if got := record(t, dbc, m, "ca-east", down); got != "opened" {
		t.Fatalf("2/3 down: got %q, want opened", got)
	}
	open := openIncidents(t, dbc, m.ID)
	if len(open) != 1 {
		t.Fatalf("open incidents = %d, want 1", len(open))
	}
	msg := open[0].Message
	if msg == nil || !strings.Contains(*msg, "eu-west-fr") || !strings.Contains(*msg, "ca-east") || !strings.Contains(*msg, "2/3") {
		t.Fatalf("incident message %v should name both failing regions and the 2/3 count", msg)
	}

	// The healthy third region reporting again must not resolve anything.
	if got := record(t, dbc, m, "eu-west-de", up); got != "none" {
		t.Fatalf("healthy minority report: got %q, want none", got)
	}

	if got := record(t, dbc, m, "eu-west-fr", up); got != "resolved" {
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
		"ca-east", "eu-west-fr")
	seedHeartbeats(t, dbc, "ca-east", "eu-west-fr")

	down := &models.CheckResult{Status: models.StatusDOWN, Message: "refused"}
	up := &models.CheckResult{Status: models.StatusUP, Message: "ok"}

	record(t, dbc, m, "eu-west-fr", down)
	if got := record(t, dbc, m, "ca-east", down); got != "opened" {
		t.Fatalf("2/2 down: got %q, want opened", got)
	}

	// eu-west-fr's pool dies: its DOWN row goes stale (interval is 60s, so 1h is
	// far past the 180s freshness window).
	if _, err := dbc.DB.NewRaw(
		`UPDATE monitor_region_status SET checked_at = now() - interval '1 hour'
		  WHERE monitor_id = ? AND region = 'eu-west-fr'`, m.ID,
	).Exec(ctx); err != nil {
		t.Fatalf("backdate region status: %v", err)
	}

	// With eu-west-fr stale, ca-east UP means 0 fresh unhealthy regions -> resolve.
	if got := record(t, dbc, m, "ca-east", up); got != "resolved" {
		t.Fatalf("recovery with stale region: got %q, want resolved", got)
	}
	// And a lone ca-east DOWN afterwards is 1/2 — not a majority.
	if got := record(t, dbc, m, "ca-east", down); got != "none" {
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
		Interval:  ptrInt(60),
		Timeout:   30,
		Enabled:   true,
		Regions:   []string{"ca-east"},
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
		Interval:  models.EffectiveInterval(m.Interval, "FREE", m.Timeout),
		Timeout:   m.Timeout,
		Enabled:   m.Enabled,
		Regions:   m.Regions,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
	seedHeartbeats(t, dbc, "ca-east")
	if got := record(t, dbc, monModel, "ca-east", &models.CheckResult{
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
	if got := record(t, dbc, monModel, "ca-east", &models.CheckResult{
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
		Regions:   []string{"ca-east"},
		OrgID:     &org.ID,
		Interval:  ptrInt(60),
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
		Interval:  models.EffectiveInterval(orgMon.Interval, "FREE", orgMon.Timeout),
		Timeout:   orgMon.Timeout,
		Enabled:   orgMon.Enabled,
		Regions:   orgMon.Regions,
		CreatedAt: orgMon.CreatedAt,
		UpdatedAt: orgMon.UpdatedAt,
	}

	if got := record(t, dbc, orgMonModel, "ca-east", &models.CheckResult{
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

// Org alert recipients (notify-only alerting seats, migration 20260714120000):
// an incident transition on an org monitor fans out to the org's recipients in
// addition to the owner's channels, skipping a recipient that duplicates an
// effectively-enabled owner channel. Solo monitors never hit recipients, and a
// recipient-sourced row finalizes as a plain delete (no history) so the
// pre-change dispatcher remains compatible.
func TestOrgRecipientFanOut(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	store := bundb.NewBunStore(dbc)

	owner := fmt.Sprintf("it-user-rcpt-%d", time.Now().UnixNano())
	org := &bundb.Organization{
		ID:        uuid.NewString(),
		Name:      fmt.Sprintf("it-org-rcpt-%d", time.Now().UnixNano()),
		OwnerID:   owner,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, err := dbc.DB.NewInsert().Model(org).Exec(ctx); err != nil {
		t.Fatalf("create org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.DB.NewDelete().Table("organizations").Where("id = ?", org.ID).Exec(ctx)
	})

	// Owner channel sharing a destination with the EMAIL recipient below: the
	// fan-out must dedupe it.
	ownerCh := &bundb.NotificationChannel{
		ID:        uuid.NewString(),
		UserID:    owner,
		Channel:   string(models.AlertChannelEMAIL),
		Target:    "shared@example.com",
		Enabled:   true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, err := dbc.DB.NewInsert().Model(ownerCh).Exec(ctx); err != nil {
		t.Fatalf("create owner channel: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dbc.DB.NewDelete().Table("notification_channels").Where("id = ?", ownerCh.ID).Exec(ctx)
	})

	dupEmail, err := store.CreateOrgAlertRecipient(ctx, org.ID, string(models.AlertChannelEMAIL), "shared@example.com")
	if err != nil {
		t.Fatalf("create email recipient: %v", err)
	}
	sms, err := store.CreateOrgAlertRecipient(ctx, org.ID, string(models.AlertChannelSMS), "+12125551234")
	if err != nil {
		t.Fatalf("create sms recipient: %v", err)
	}

	newMonitor := func(name string, orgID *string) (*bundb.Monitor, *models.Monitor) {
		m := &bundb.Monitor{
			ID:        uuid.NewString(),
			UserID:    owner,
			OrgID:     orgID,
			Name:      name,
			Type:      string(models.MonitorTypeHTTP),
			Target:    "https://example.com",
			Interval:  ptrInt(60),
			Timeout:   30,
			Enabled:   true,
			Regions:   []string{"ca-east"},
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		if _, err := dbc.DB.NewInsert().Model(m).Exec(ctx); err != nil {
			t.Fatalf("create monitor %s: %v", name, err)
		}
		t.Cleanup(func() {
			_, _ = dbc.DB.NewDelete().Table("monitors").Where("id = ?", m.ID).Exec(ctx)
		})
		return m, &models.Monitor{
			ID:        m.ID,
			OrgID:     m.OrgID,
			Name:      m.Name,
			Type:      models.MonitorType(m.Type),
			Target:    m.Target,
			Interval:  models.EffectiveInterval(m.Interval, "FREE", m.Timeout),
			Timeout:   m.Timeout,
			Enabled:   m.Enabled,
			Regions:   m.Regions,
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
		}
	}

	orgMon, orgMonModel := newMonitor("it-monitor-rcpt-org", &org.ID)
	soloMon, soloMonModel := newMonitor("it-monitor-rcpt-solo", nil)

	seedHeartbeats(t, dbc, "ca-east")

	if got := record(t, dbc, orgMonModel, "ca-east", &models.CheckResult{
		Status: models.StatusDOWN, Message: "down",
	}); got != "opened" {
		t.Fatalf("org open: got %q, want opened", got)
	}

	var rows []bundb.AlertOutbox
	if err := dbc.DB.NewSelect().
		Model(&rows).
		Where("monitor_id = ?", orgMon.ID).
		Scan(ctx); err != nil {
		t.Fatalf("list org outbox: %v", err)
	}
	// Expect exactly two rows: the owner's EMAIL channel and the SMS
	// recipient. The EMAIL recipient duplicates the owner channel's
	// destination and must be deduped.
	if len(rows) != 2 {
		t.Fatalf("org outbox rows = %d, want 2 (owner channel + sms recipient): %+v", len(rows), rows)
	}
	var smsRow *bundb.AlertOutbox
	for i := range rows {
		switch {
		case rows[i].NotificationChannelID != nil && *rows[i].NotificationChannelID == ownerCh.ID:
			// owner channel row, expected
		case rows[i].OrgAlertRecipientID != nil && *rows[i].OrgAlertRecipientID == sms.ID:
			smsRow = &rows[i]
		case rows[i].OrgAlertRecipientID != nil && *rows[i].OrgAlertRecipientID == dupEmail.ID:
			t.Fatalf("duplicate EMAIL recipient was not deduped against the owner channel")
		default:
			t.Fatalf("unexpected outbox row: %+v", rows[i])
		}
	}
	if smsRow == nil {
		t.Fatalf("no outbox row for the SMS recipient")
	}
	if smsRow.Channel != string(models.AlertChannelSMS) || smsRow.Target != "+12125551234" {
		t.Fatalf("sms row channel/target = %s/%s, want SMS/+12125551234", smsRow.Channel, smsRow.Target)
	}
	if smsRow.AlertID != nil || smsRow.NotificationChannelID != nil {
		t.Fatalf("recipient row must have NULL alert_id and notification_channel_id: %+v", smsRow)
	}

	// A recipient-sourced row finalizes as a plain delete: no history row, the
	// exact behavior an old dispatcher binary exhibits (nil/nil source ids).
	if err := store.FinalizeOutboxAlert(ctx, smsRow.ID, models.StatusDOWN, "delivered", nil, nil); err != nil {
		t.Fatalf("finalize recipient row: %v", err)
	}
	var left int
	if err := dbc.DB.NewRaw(`SELECT count(*) FROM alert_outbox WHERE id = ?`, smsRow.ID).Scan(ctx, &left); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if left != 0 {
		t.Fatalf("recipient outbox row not deleted on finalize")
	}
	var hist int
	if err := dbc.DB.NewRaw(`SELECT count(*) FROM alert_history WHERE message = 'delivered' AND alert_id IS NULL AND notification_channel_id IS NULL`).Scan(ctx, &hist); err != nil {
		t.Fatalf("count history: %v", err)
	}
	if hist != 0 {
		t.Fatalf("recipient finalize must not write history")
	}

	// Solo monitors never fan out to org recipients.
	if got := record(t, dbc, soloMonModel, "ca-east", &models.CheckResult{
		Status: models.StatusDOWN, Message: "down",
	}); got != "opened" {
		t.Fatalf("solo open: got %q, want opened", got)
	}
	var soloRecipientRows int
	if err := dbc.DB.NewRaw(
		`SELECT count(*) FROM alert_outbox WHERE monitor_id = ? AND org_alert_recipient_id IS NOT NULL`, soloMon.ID,
	).Scan(ctx, &soloRecipientRows); err != nil {
		t.Fatalf("count solo recipient rows: %v", err)
	}
	if soloRecipientRows != 0 {
		t.Fatalf("solo monitor produced %d recipient rows, want 0", soloRecipientRows)
	}
}

func TestResultWriterBatchFlush(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	m := createTestMonitor(t, dbc, fmt.Sprintf("rw-%d", time.Now().UnixNano()))

	w := newResultWriter(bundb.NewBunStore(dbc), "eu-west-fr")
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
		if row.Region != "eu-west-fr" {
			t.Fatalf("region = %q, want eu-west-fr", row.Region)
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
	w := newResultWriter(bundb.NewBunStore(dbc), "ca-east")
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
	if rows[0].Region != "ca-east" {
		t.Fatalf("region = %q, want ca-east", rows[0].Region)
	}
}

// regionStatusSource returns the stored source ('SCHEDULED'/'VERIFICATION') for
// one region's status row.
func regionStatusSource(t *testing.T, dbc *bundb.Client, monitorID, region string) string {
	t.Helper()
	var src string
	if err := dbc.DB.NewRaw(
		`SELECT source FROM monitor_region_status WHERE monitor_id = ? AND region = ?`, monitorID, region,
	).Scan(context.Background(), &src); err != nil {
		t.Fatalf("region status source: %v", err)
	}
	return src
}

// A single failing region on a single-region (FREE) monitor no longer alerts on
// its own: every other active region is asked to confirm first, and the
// incident opens only once a majority of the active regions agree. This is the
// cross-region false-positive guard extended to monitors that check from one
// region.
func TestVerificationConfirmsBeforeAlert(t *testing.T) {
	dbc := integrationDB(t)
	m := createTestMonitor(t, dbc, fmt.Sprintf("vc-%d", time.Now().UnixNano()), "ca-east")
	seedHeartbeats(t, dbc, "ca-east", "eu-west-fr", "eu-west-de")

	down := &models.CheckResult{Status: models.StatusDOWN, Message: "refused"}

	// ca-east reports DOWN: 1 of 3 active regions — a minority, so no incident,
	// but the other two active regions are queued to confirm.
	if got := record(t, dbc, m, "ca-east", down); got != "none" {
		t.Fatalf("origin DOWN: got %q, want none (awaiting confirmation)", got)
	}
	if n := len(openIncidents(t, dbc, m.ID)); n != 0 {
		t.Fatalf("open incidents before confirmation = %d, want 0", n)
	}
	if pend := pendingVerifications(t, dbc, m.ID); len(pend) != 2 ||
		pend[0] != "eu-west-de" || pend[1] != "eu-west-fr" {
		t.Fatalf("pending confirmations = %v, want [eu-west-de eu-west-fr]", pend)
	}

	// eu-west-fr confirms DOWN: 2 of 3 — majority — opens the incident.
	if got := recordVerify(t, dbc, m.ID, "eu-west-fr", down); got != "opened" {
		t.Fatalf("confirmation DOWN: got %q, want opened", got)
	}
	// The confirming region (not one this monitor normally checks) is recorded
	// as VERIFICATION so it stays out of the per-region status UI.
	if src := regionStatusSource(t, dbc, m.ID, "eu-west-fr"); src != "VERIFICATION" {
		t.Fatalf("eu-west-fr source = %q, want VERIFICATION", src)
	}
	if src := regionStatusSource(t, dbc, m.ID, "ca-east"); src != "SCHEDULED" {
		t.Fatalf("ca-east source = %q, want SCHEDULED", src)
	}
}

// A genuinely regional blip — the origin sees DOWN but every other region
// confirms UP — must never open an incident.
func TestVerificationRegionalBlipNeverAlerts(t *testing.T) {
	dbc := integrationDB(t)
	m := createTestMonitor(t, dbc, fmt.Sprintf("blip-%d", time.Now().UnixNano()), "ca-east")
	seedHeartbeats(t, dbc, "ca-east", "eu-west-fr", "eu-west-de")

	down := &models.CheckResult{Status: models.StatusDOWN, Message: "refused"}
	up := &models.CheckResult{Status: models.StatusUP, Message: "ok"}

	if got := record(t, dbc, m, "ca-east", down); got != "none" {
		t.Fatalf("origin DOWN: got %q, want none", got)
	}
	if got := recordVerify(t, dbc, m.ID, "eu-west-fr", up); got != "none" {
		t.Fatalf("eu-west-fr UP: got %q, want none", got)
	}
	if got := recordVerify(t, dbc, m.ID, "eu-west-de", up); got != "none" {
		t.Fatalf("eu-west-de UP: got %q, want none", got)
	}
	if n := len(openIncidents(t, dbc, m.ID)); n != 0 {
		t.Fatalf("open incidents after all-confirm-UP = %d, want 0", n)
	}
}

// If no region answers the confirmation request, the request eventually expires
// and the fallback sweep re-evaluates quorum without it — so a real outage that
// silences the peer regions still alerts, on a bounded delay rather than never.
func TestVerificationExpiryFallbackAlerts(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	m := createTestMonitor(t, dbc, fmt.Sprintf("exp-%d", time.Now().UnixNano()), "ca-east")
	seedHeartbeats(t, dbc, "ca-east", "eu-west-fr", "eu-west-de")
	store := bundb.NewBunStore(dbc)

	if got := record(t, dbc, m, "ca-east", &models.CheckResult{Status: models.StatusDOWN, Message: "refused"}); got != "none" {
		t.Fatalf("origin DOWN: got %q, want none", got)
	}

	// Nobody confirms; force the requests past expiry and run the sweep.
	if _, err := dbc.DB.NewRaw(
		`UPDATE region_verification_requests SET expires_at = now() - interval '1 second' WHERE monitor_id = ?`, m.ID,
	).Exec(ctx); err != nil {
		t.Fatalf("expire requests: %v", err)
	}
	ids, err := store.ExpireVerificationRequests(ctx)
	if err != nil {
		t.Fatalf("ExpireVerificationRequests: %v", err)
	}
	found := false
	for _, id := range ids {
		if id == m.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expired monitor ids %v missing %s", ids, m.ID)
	}
	// With the unanswered confirmations gone from the denominator, the origin's
	// DOWN is now a majority of the reporting set → opens.
	if got, err := store.EvaluateMonitorQuorum(ctx, m.ID); err != nil || got != "opened" {
		t.Fatalf("EvaluateMonitorQuorum after expiry: got %q err %v, want opened", got, err)
	}
}

// On resolve, verification artifacts are purged so a stale confirmation DOWN
// can't linger and combine with a later single-region blip into a false reopen.
func TestVerificationResolveClearsConfirmations(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	m := createTestMonitor(t, dbc, fmt.Sprintf("rc-%d", time.Now().UnixNano()), "ca-east")
	seedHeartbeats(t, dbc, "ca-east", "eu-west-fr", "eu-west-de")

	down := &models.CheckResult{Status: models.StatusDOWN, Message: "refused"}

	record(t, dbc, m, "ca-east", down)
	if got := recordVerify(t, dbc, m.ID, "eu-west-fr", down); got != "opened" {
		t.Fatalf("confirm DOWN: got %q, want opened", got)
	}

	// Origin recovers → below majority → resolve, and confirmations are purged.
	if got := record(t, dbc, m, "ca-east", &models.CheckResult{Status: models.StatusUP, Message: "ok"}); got != "resolved" {
		t.Fatalf("recovery: got %q, want resolved", got)
	}
	var verificationRows, requests int
	_ = dbc.DB.NewRaw(`SELECT count(*) FROM monitor_region_status WHERE monitor_id = ? AND source = 'VERIFICATION'`, m.ID).Scan(ctx, &verificationRows)
	_ = dbc.DB.NewRaw(`SELECT count(*) FROM region_verification_requests WHERE monitor_id = ?`, m.ID).Scan(ctx, &requests)
	if verificationRows != 0 || requests != 0 {
		t.Fatalf("after resolve: verification rows=%d requests=%d, want 0/0", verificationRows, requests)
	}
}

// A follow-plan monitor (NULL interval) resolves its interval from the owner's
// current plan at fetch time: no per-monitor write, so a plan change takes
// effect on the next scheduler sync.
func TestFollowPlanIntervalResolvesAtFetch(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	owner := fmt.Sprintf("it-user-fp-%d", time.Now().UnixNano())
	region := "ca-east"

	m := &bundb.Monitor{
		ID:        uuid.NewString(),
		UserID:    owner,
		Name:      "it-monitor-fp",
		Type:      string(models.MonitorTypeHTTP),
		Target:    "https://example.com",
		Interval:  nil, // follow plan
		Timeout:   30,
		Enabled:   true,
		Regions:   []string{region},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, err := dbc.DB.NewInsert().Model(m).Exec(ctx); err != nil {
		t.Fatalf("create monitor: %v", err)
	}
	t.Cleanup(func() { _, _ = dbc.DB.NewDelete().Table("monitors").Where("id = ?", m.ID).Exec(ctx) })

	store := bundb.NewBunStore(dbc)
	find := func() *models.Monitor {
		mons, err := store.FetchActiveMonitors(ctx, region)
		if err != nil {
			t.Fatalf("FetchActiveMonitors: %v", err)
		}
		for i := range mons {
			if mons[i].ID == m.ID {
				return &mons[i]
			}
		}
		t.Fatalf("monitor %s not fetched", m.ID)
		return nil
	}

	// No subscription → FREE → 300.
	if got := find(); got.Interval != 300 || got.IntervalIsCustom {
		t.Fatalf("FREE follow-plan: interval=%d custom=%v, want 300/false", got.Interval, got.IntervalIsCustom)
	}

	// Upgrade to PRO → 60, with no write to the monitor row.
	sub := &bundb.Subscription{
		ID:        uuid.NewString(),
		UserID:    owner,
		Plan:      "PRO",
		Status:    "ACTIVE",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, err := dbc.DB.NewInsert().Model(sub).Exec(ctx); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	t.Cleanup(func() { _, _ = dbc.DB.NewDelete().Table("subscriptions").Where("id = ?", sub.ID).Exec(ctx) })

	if got := find(); got.Interval != 60 || got.IntervalIsCustom {
		t.Fatalf("PRO follow-plan: interval=%d custom=%v, want 60/false", got.Interval, got.IntervalIsCustom)
	}
}

// The plan-floor constants duplicated into maintenance.effective_interval must
// stay equal to models.LimitsForPlan — this pins the one place SQL and Go share
// the numbers.
func TestEffectiveIntervalSQLMatchesGo(t *testing.T) {
	dbc := integrationDB(t)
	ctx := context.Background()
	for _, plan := range []string{"FREE", "PRO", "ENTERPRISE"} {
		var got int
		if err := dbc.DB.NewRaw(`SELECT maintenance.effective_interval(NULL::int, ?)`, plan).Scan(ctx, &got); err != nil {
			t.Fatalf("effective_interval(%s): %v", plan, err)
		}
		if want := models.LimitsForPlan(plan).MinInterval; got != want {
			t.Fatalf("effective_interval(NULL, %q) = %d, want %d (models.LimitsForPlan)", plan, got, want)
		}
	}
}
