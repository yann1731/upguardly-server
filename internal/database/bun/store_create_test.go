package bun

// DB-backed regression tests for the Create*/Upsert* insert paths.
//
// Prisma generated `id` (@default(cuid())) and `updated_at` (@updatedAt) on the
// client, so the columns carry no Postgres default. When Prisma was removed the
// bun inserts kept excluding those columns — handing Postgres a NULL id and a
// NULL updated_at — and every insert failed with a NOT NULL violation (23502).
// The mockStore-based handler tests could not catch it; only real SQL can.
//
// Needs a real Postgres with migrations applied — see store_reconcile_test.go
// for the setup, and run with BUN_TEST_DATABASE_URL set.

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"upguardly-backend/internal/models"
)

func TestCreatePathsPopulateGeneratedColumns(t *testing.T) {
	ctx := context.Background()
	s := reconcileTestStore(t)
	user := "create-" + uuid.NewString()

	t.Run("monitor", func(t *testing.T) {
		m, err := s.CreateMonitor(ctx, user, "", "mon-"+uuid.NewString()[:8], "HTTP", "http://93.184.216.34", nil, 30, true, []string{"ca-east"})
		if err != nil {
			t.Fatalf("CreateMonitor: %v", err)
		}
		if m.ID == "" {
			t.Error("monitor id is empty")
		}
	})

	t.Run("organization", func(t *testing.T) {
		o, err := s.CreateOrganization(ctx, user, "org-"+uuid.NewString()[:8])
		if err != nil {
			t.Fatalf("CreateOrganization: %v", err)
		}
		if o.ID == "" {
			t.Error("organization id is empty")
		}
	})

	t.Run("notification channel", func(t *testing.T) {
		nc, err := s.CreateNotificationChannel(ctx, user, "EMAIL", user+"@example.com", true)
		if err != nil {
			t.Fatalf("CreateNotificationChannel: %v", err)
		}
		if nc.ID == "" {
			t.Error("notification channel id is empty")
		}
	})

	// The Stripe webhook path: a NULL id failed here before conflict resolution
	// even ran, so subscription writes never landed.
	t.Run("subscription upsert", func(t *testing.T) {
		sub, err := s.UpsertSubscription(ctx, models.UpsertSubscriptionParams{
			UserID: user,
			Plan:   "PRO",
			Status: "ACTIVE",
		})
		if err != nil {
			t.Fatalf("UpsertSubscription (insert): %v", err)
		}
		if sub.ID == "" {
			t.Error("subscription id is empty")
		}

		// Second call must take the ON CONFLICT branch, not insert a new row.
		again, err := s.UpsertSubscription(ctx, models.UpsertSubscriptionParams{
			UserID: user,
			Plan:   "ENTERPRISE",
			Status: "ACTIVE",
		})
		if err != nil {
			t.Fatalf("UpsertSubscription (conflict): %v", err)
		}
		if again.ID != sub.ID {
			t.Errorf("upsert created a second row: got id %q, want %q", again.ID, sub.ID)
		}
		if again.Plan != "ENTERPRISE" {
			t.Errorf("upsert did not update plan: got %q, want ENTERPRISE", again.Plan)
		}
	})
}
