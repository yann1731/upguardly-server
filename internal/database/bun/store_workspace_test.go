package bun

// DB-backed tests for workspace scoping of monitors: ListMonitors returns one
// workspace at a time, and monitorAccessClause grants org monitors through
// current membership only. Run with BUN_TEST_DATABASE_URL set (see
// store_reconcile_test.go).

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"upguardly-backend/internal/models"
)

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
		m, err := s.CreateMonitor(ctx, member, orgID, name, "HTTP", "http://93.184.216.34", nil, 30, nil, nil, nil, true, []string{"ca-east"})
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
