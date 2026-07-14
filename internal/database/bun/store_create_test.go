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
	"time"

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
	t.Run("org alert recipient", func(t *testing.T) {
		o, err := s.CreateOrganization(ctx, "rcpt-"+uuid.NewString(), "org-"+uuid.NewString()[:8])
		if err != nil {
			t.Fatalf("CreateOrganization: %v", err)
		}
		r, err := s.CreateOrgAlertRecipient(ctx, o.ID, "EMAIL", "oncall@example.com")
		if err != nil {
			t.Fatalf("CreateOrgAlertRecipient: %v", err)
		}
		if r.ID == "" {
			t.Error("org alert recipient id is empty")
		}

		// The unique (org, channel, target) index must surface as ErrConflict.
		if _, err := s.CreateOrgAlertRecipient(ctx, o.ID, "EMAIL", "oncall@example.com"); err != models.ErrConflict {
			t.Errorf("duplicate recipient: got %v, want ErrConflict", err)
		}
	})

	// Login seats: the transactional re-check in AcceptInvitation must let all
	// invited members convert their own pending invitations (each accept
	// excludes itself from the count) but reject an accept once the org is
	// full — the race the handler pre-check can't close.
	t.Run("accept invitation seat limit", func(t *testing.T) {
		owner := "seat-owner-" + uuid.NewString()
		o, err := s.CreateOrganization(ctx, owner, "org-"+uuid.NewString()[:8])
		if err != nil {
			t.Fatalf("CreateOrganization: %v", err)
		}

		invite := func(n int) string {
			token := "tok-" + uuid.NewString()
			_, err := s.CreateInvitation(ctx, o.ID, uuid.NewString()[:8]+"@example.com", owner,
				models.OrgRoleMember, token, time.Now().Add(24*time.Hour))
			if err != nil {
				t.Fatalf("CreateInvitation %d: %v", n, err)
			}
			return token
		}

		// Three invitations fill the plan's three seats; each accept must
		// still succeed because an accept converts its own seat.
		for i, token := range []string{invite(1), invite(2), invite(3)} {
			if _, err := s.AcceptInvitation(ctx, token, "seat-user-"+uuid.NewString(), 3); err != nil {
				t.Fatalf("AcceptInvitation %d at cap-with-self: %v", i+1, err)
			}
		}

		// A fourth invitation (simulating one that raced past the handler
		// pre-check) must be rejected at accept time.
		if _, err := s.AcceptInvitation(ctx, invite(4), "seat-user-"+uuid.NewString(), 3); err != models.ErrSeatLimit {
			t.Errorf("AcceptInvitation over cap: got %v, want ErrSeatLimit", err)
		}
	})

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
