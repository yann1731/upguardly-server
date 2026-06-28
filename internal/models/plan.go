package models

// PlanLimits describes the per-plan resource caps. A value of -1 means unlimited.
type PlanLimits struct {
	MaxMonitors         int
	MaxAlertsPerMonitor int
}

// Unlimited is the sentinel used for plans with no cap on a given resource.
const Unlimited = -1

// LimitsForPlan returns the resource limits for a subscription plan name.
// Unknown or empty plans fall back to the FREE tier.
func LimitsForPlan(plan string) PlanLimits {
	switch plan {
	case "PRO":
		return PlanLimits{MaxMonitors: 10, MaxAlertsPerMonitor: 10}
	case "ENTERPRISE":
		return PlanLimits{MaxMonitors: 100, MaxAlertsPerMonitor: Unlimited}
	default: // FREE and anything unrecognised
		return PlanLimits{MaxMonitors: 5, MaxAlertsPerMonitor: 1}
	}
}
