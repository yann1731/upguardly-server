package bun

import "testing"

// The monitor's effective plan (the org owner's for org monitors, as selected
// by ownerPlanExpr) is exposed so clients gate per-monitor features on the
// plan the monitor actually runs on, not on the viewer's own subscription.
func TestMonitorToModelCarriesOwnerPlan(t *testing.T) {
	m := &Monitor{ID: "mon-1", Type: "HTTP", Timeout: 30, OwnerPlan: "ENTERPRISE"}

	got := m.toModel()

	if got.Plan != "ENTERPRISE" {
		t.Fatalf("Plan = %q, want ENTERPRISE", got.Plan)
	}
}
