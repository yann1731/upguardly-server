package bun

// DB-backed tests for workspace scoping of monitors: ListMonitors returns one
// workspace at a time, and monitorAccessClause grants org monitors through
// current membership only. Run with BUN_TEST_DATABASE_URL set (see
// store_reconcile_test.go).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"upguardly-backend/internal/models"
)

// newMonitorParams is the shape most DB tests want: a monitor in one workspace
// with the quota check disabled, so a test about scoping or column defaults
// doesn't have to set up a subscription. Tests that are *about* the quota set
// BillingOwnerID and MaxMonitors themselves.
func newMonitorParams(userID, orgID, name string) models.CreateMonitorParams {
	return models.CreateMonitorParams{
		UserID:      userID,
		OrgID:       orgID,
		Name:        name,
		Type:        "HTTP",
		Target:      "http://93.184.216.34",
		Timeout:     30,
		Enabled:     true,
		Regions:     []string{"ca-east"},
		MaxMonitors: models.Unlimited,
	}
}

func TestMonitorWorkspaceScoping(t *testing.T) {
	ctx := context.Background()
	s := reconcileTestStore(t)
	owner := "ws-owner-" + uuid.NewString()
	member := "ws-member-" + uuid.NewString()

	org, err := s.CreateOrganization(ctx, owner, "ws-org-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteOrganization(context.Background(), org.ID) })

	if _, err := s.client.DB.NewInsert().Model(&OrganizationMember{
		ID: uuid.NewString(), OrganizationID: org.ID, UserID: member, Role: string(models.OrgRoleMember),
	}).Exec(ctx); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	create := func(orgID, name string) string {
		t.Helper()
		m, err := s.CreateMonitor(ctx, newMonitorParams(member, orgID, name))
		if err != nil {
			t.Fatalf("CreateMonitor(%s): %v", name, err)
		}
		t.Cleanup(func() {
			_, _ = s.client.DB.NewDelete().Model((*Monitor)(nil)).Where("id = ?", m.ID).Exec(context.Background())
		})
		return m.ID
	}
	orgMon := create(org.ID, "ws-org-mon")
	soloMon := create("", "ws-solo-mon")

	ids := func(userID, orgID string) []string {
		t.Helper()
		ms, err := s.ListMonitors(ctx, userID, orgID)
		if err != nil {
			t.Fatalf("ListMonitors(%s, %q): %v", userID, orgID, err)
		}
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = m.ID
		}
		return out
	}
	reachable := func(id, userID string) bool {
		t.Helper()
		_, err := s.GetMonitor(ctx, id, userID)
		if err != nil && !errors.Is(err, models.ErrNotFound) {
			t.Fatalf("GetMonitor: %v", err)
		}
		return err == nil
	}

	if got := ids(member, ""); len(got) != 1 || got[0] != soloMon {
		t.Errorf("member personal workspace = %v, want only the solo monitor %s", got, soloMon)
	}
	if got := ids(member, org.ID); len(got) != 1 || got[0] != orgMon {
		t.Errorf("member org workspace = %v, want only the org monitor %s", got, orgMon)
	}
	if got := ids(owner, org.ID); len(got) != 1 || got[0] != orgMon {
		t.Errorf("owner org workspace = %v, want the org monitor %s", got, orgMon)
	}
	if got := ids(owner, ""); len(got) != 0 {
		t.Errorf("owner personal workspace = %v, want none (the member's solo monitor isn't theirs)", got)
	}

	if !reachable(orgMon, owner) {
		t.Error("owner can't reach the org monitor a member created")
	}
	if reachable(soloMon, owner) {
		t.Error("owner can reach a member's solo monitor")
	}

	if err := s.RemoveMember(ctx, org.ID, member); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if reachable(orgMon, member) {
		t.Error("a removed member can still reach the org monitor they created")
	}
	if !reachable(soloMon, member) {
		t.Error("a removed member lost their own solo monitor")
	}
}

// TestCountMonitorsForBillingOwner pins billingScopeClause: a plan's monitor
// budget is one pool per billing owner, covering their personal monitors plus
// every monitor in an org they own — including monitors other members created
// there. A member's own pool must not pick any of that up.
func TestCountMonitorsForBillingOwner(t *testing.T) {
	ctx := context.Background()
	s := reconcileTestStore(t)
	owner := "pool-owner-" + uuid.NewString()
	member := "pool-member-" + uuid.NewString()
	stranger := "pool-stranger-" + uuid.NewString()

	org, err := s.CreateOrganization(ctx, owner, "pool-org-"+uuid.NewString()[:8])
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	t.Cleanup(func() { _ = s.DeleteOrganization(context.Background(), org.ID) })

	if _, err := s.client.DB.NewInsert().Model(&OrganizationMember{
		ID: uuid.NewString(), OrganizationID: org.ID, UserID: member, Role: string(models.OrgRoleMember),
	}).Exec(ctx); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	create := func(userID, orgID, name string) {
		t.Helper()
		m, err := s.CreateMonitor(ctx, newMonitorParams(userID, orgID, name))
		if err != nil {
			t.Fatalf("CreateMonitor(%s): %v", name, err)
		}
		t.Cleanup(func() {
			_, _ = s.client.DB.NewDelete().Model((*Monitor)(nil)).Where("id = ?", m.ID).Exec(context.Background())
		})
	}

	create(owner, "", "pool-owner-solo-1")
	create(owner, "", "pool-owner-solo-2")
	create(owner, org.ID, "pool-org-by-owner")
	// An org monitor the member created still bills to the owner.
	create(member, org.ID, "pool-org-by-member")
	// The member's own monitor bills to the member.
	create(member, "", "pool-member-solo")
	// An unrelated account is in nobody's pool.
	create(stranger, "", "pool-stranger-solo")

	count := func(ownerID string) int {
		t.Helper()
		n, err := s.CountMonitorsForBillingOwner(ctx, ownerID)
		if err != nil {
			t.Fatalf("CountMonitorsForBillingOwner(%s): %v", ownerID, err)
		}
		return n
	}

	if got := count(owner); got != 4 {
		t.Errorf("owner pool = %d, want 4 (2 solo + 2 org, one of them created by the member)", got)
	}
	if got := count(member); got != 1 {
		t.Errorf("member pool = %d, want 1 (their own solo monitor only)", got)
	}
	if got := count(stranger); got != 1 {
		t.Errorf("stranger pool = %d, want 1", got)
	}
}

// TestCreateMonitorEnforcesPooledLimit covers the quota check CreateMonitor
// runs inside its own transaction: the cap is refused whichever workspace the
// monitors filling the pool sit in, and concurrent creates at the boundary
// can't overshoot it (they used to — the count and the insert were separate
// statements).
func TestCreateMonitorEnforcesPooledLimit(t *testing.T) {
	ctx := context.Background()
	s := reconcileTestStore(t)

	t.Run("org monitors consume the owner's personal budget", func(t *testing.T) {
		owner := "cap-owner-" + uuid.NewString()
		org, err := s.CreateOrganization(ctx, owner, "cap-org-"+uuid.NewString()[:8])
		if err != nil {
			t.Fatalf("CreateOrganization: %v", err)
		}
		t.Cleanup(func() { _ = s.DeleteOrganization(context.Background(), org.ID) })

		capped := func(userID, orgID, name string, max int) error {
			p := newMonitorParams(userID, orgID, name)
			p.BillingOwnerID = owner
			p.MaxMonitors = max
			m, err := s.CreateMonitor(ctx, p)
			if err == nil {
				t.Cleanup(func() {
					_, _ = s.client.DB.NewDelete().Model((*Monitor)(nil)).Where("id = ?", m.ID).Exec(context.Background())
				})
			}
			return err
		}

		// Fill a cap of 2 from both sides of the pool.
		if err := capped(owner, "", "cap-solo", 2); err != nil {
			t.Fatalf("first create: %v", err)
		}
		if err := capped(owner, org.ID, "cap-org", 2); err != nil {
			t.Fatalf("second create: %v", err)
		}
		// The pool is full, so neither workspace may take a third.
		if err := capped(owner, org.ID, "cap-org-2", 2); !errors.Is(err, models.ErrMonitorLimit) {
			t.Errorf("org create over the pooled cap: got %v, want ErrMonitorLimit", err)
		}
		if err := capped(owner, "", "cap-solo-2", 2); !errors.Is(err, models.ErrMonitorLimit) {
			t.Errorf("personal create over the pooled cap: got %v, want ErrMonitorLimit", err)
		}
	})

	t.Run("a create waits on the billing owner's quota lock", func(t *testing.T) {
		// The count and the insert are only safe together because they run
		// under pg_advisory_xact_lock keyed on the billing owner. Hold that
		// exact lock from another transaction and the create must block; the
		// timing-independent half of the assertion is that it completes once
		// the lock is released. Without the lock in CreateMonitor this fails
		// at the first check: the create sails past a held lock.
		owner := "lock-owner-" + uuid.NewString()

		blocker, err := s.client.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin blocker: %v", err)
		}
		defer func() { _ = blocker.Rollback() }()
		if _, err := blocker.NewRaw(
			"SELECT pg_advisory_xact_lock(hashtextextended('monitor_quota:' || ?, 0))", owner,
		).Exec(ctx); err != nil {
			t.Fatalf("take blocking lock: %v", err)
		}

		done := make(chan error, 1)
		var createdID string
		go func() {
			p := newMonitorParams(owner, "", "lock-"+uuid.NewString()[:8])
			p.BillingOwnerID = owner
			p.MaxMonitors = 1
			m, err := s.CreateMonitor(context.Background(), p)
			if err == nil {
				createdID = m.ID
			}
			done <- err
		}()

		select {
		case err := <-done:
			t.Fatalf("create finished while the owner's quota lock was held (err=%v) — it is not serializing", err)
		case <-time.After(250 * time.Millisecond):
		}

		if err := blocker.Rollback(); err != nil {
			t.Fatalf("release blocking lock: %v", err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("create after the lock was released: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("create never completed after the lock was released")
		}
		t.Cleanup(func() {
			_, _ = s.client.DB.NewDelete().Model((*Monitor)(nil)).Where("id = ?", createdID).Exec(context.Background())
		})
	})
}
