package bun

// DB-backed regression tests for ReconcileMonitorsToPlan. This is the path that
// re-snaps existing monitors to a plan's limits on upgrade/downgrade; it runs
// raw SQL whose positional arguments were once mis-ordered (the SET value was
// dropped, shifting user_id into the integer `interval` column), which the
// mockStore-based handler tests could not catch. These exercise the real SQL.
//
// Needs a real Postgres with migrations applied. Skipped unless a database URL
// is provided, e.g. reusing the scheduler integration DB:
//
//	docker run -d --name pgtest -e POSTGRES_PASSWORD=test -e POSTGRES_DB=upguardly -p 55433:5432 postgres:18-alpine
//	DATABASE_URL="postgresql://postgres:test@localhost:55433/upguardly?sslmode=disable" \
//	  go run github.com/steebchen/prisma-client-go migrate deploy
//	BUN_TEST_DATABASE_URL="postgresql://postgres:test@localhost:55433/upguardly?sslmode=disable" \
//	  go test ./internal/database/bun/ -run Reconcile -v

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"upguardly-backend/internal/models"
)

func reconcileTestStore(t *testing.T) *BunStore {
	t.Helper()
	url := os.Getenv("BUN_TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("SCHEDULER_TEST_DATABASE_URL")
	}
	if url == "" {
		t.Skip("BUN_TEST_DATABASE_URL (or SCHEDULER_TEST_DATABASE_URL) not set")
	}
	client := NewClient(url)
	if err := client.Connect(); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect() })
	return NewBunStore(client)
}

func insertMonitor(t *testing.T, s *BunStore, userID string, interval, timeout int, regions []string) string {
	t.Helper()
	ctx := context.Background()
	m := &Monitor{
		ID:        uuid.NewString(),
		UserID:    userID,
		Name:      "reconcile-" + uuid.NewString()[:8],
		Type:      string(models.MonitorTypeHTTP),
		Target:    "https://example.com",
		Interval:  &interval,
		Timeout:   timeout,
		Enabled:   true,
		Regions:   regions,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if _, err := s.client.DB.NewInsert().Model(m).Exec(ctx); err != nil {
		t.Fatalf("insert monitor: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.client.DB.NewDelete().Table("monitors").Where("id = ?", m.ID).Exec(ctx)
	})
	return m.ID
}

func monitorRow(t *testing.T, s *BunStore, id string) *Monitor {
	t.Helper()
	m := new(Monitor)
	if err := s.client.DB.NewSelect().Model(m).Where("id = ?", id).Scan(context.Background()); err != nil {
		t.Fatalf("select monitor: %v", err)
	}
	return m
}

// Upgrade FREE->PRO must NOT touch explicit interval overrides: follow-plan
// monitors (interval IS NULL) re-resolve at read time, and an override is
// never lowered on upgrade — the user chose that value deliberately.
func TestReconcileUpgradeLeavesOverrides(t *testing.T) {
	s := reconcileTestStore(t)
	ctx := context.Background()
	user := "reconcile-up-" + uuid.NewString()

	pinned := insertMonitor(t, s, user, 300, 30, []string{"ca-east"})

	n, err := s.ReconcileMonitorsToPlan(ctx, user, "FREE", "PRO")
	if err != nil {
		t.Fatalf("reconcile FREE->PRO: %v", err)
	}
	if n != 0 {
		t.Errorf("adjusted monitors: got %d, want 0", n)
	}
	if got := monitorRow(t, s, pinned).Interval; got == nil || *got != 300 {
		t.Errorf("pinned monitor interval: got %v, want 300 (unchanged)", got)
	}
}

// Downgrade PRO->FREE must raise any sub-floor monitor back up to the FREE
// floor (300s).
func TestReconcileDowngradeRaisesInterval(t *testing.T) {
	s := reconcileTestStore(t)
	ctx := context.Background()
	user := "reconcile-down-" + uuid.NewString()

	fast := insertMonitor(t, s, user, 60, 30, []string{"ca-east"})

	n, err := s.ReconcileMonitorsToPlan(ctx, user, "PRO", "FREE")
	if err != nil {
		t.Fatalf("reconcile PRO->FREE: %v", err)
	}
	if n != 1 {
		t.Errorf("adjusted monitors: got %d, want 1", n)
	}
	if got := monitorRow(t, s, fast).Interval; got == nil || *got != 300 {
		t.Errorf("fast monitor interval: got %v, want 300", got)
	}
}

// Downgrade to a plan with a region cap must trim over-cap region lists. FREE
// caps monitors at a single region.
func TestReconcileDowngradeTrimsRegions(t *testing.T) {
	s := reconcileTestStore(t)
	ctx := context.Background()
	user := "reconcile-region-" + uuid.NewString()

	multi := insertMonitor(t, s, user, 60, 30, []string{"ca-east", "eu-west-fr"})

	if _, err := s.ReconcileMonitorsToPlan(ctx, user, "PRO", "FREE"); err != nil {
		t.Fatalf("reconcile PRO->FREE: %v", err)
	}
	if got := monitorRow(t, s, multi).Regions; len(got) != 1 {
		t.Errorf("region count after trim: got %d (%v), want 1", len(got), got)
	}
}
