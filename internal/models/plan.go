package models

// PlanLimits describes the per-plan resource caps. A value of -1 means unlimited.
type PlanLimits struct {
	MaxMonitors         int
	MaxAlertsPerMonitor int
	// MaxGlobalChannels caps the account-level notification channels.
	MaxGlobalChannels int
	// MinInterval is the smallest allowed check interval (in seconds) for the
	// plan. Lower tiers are throttled to longer intervals to bound load.
	MinInterval int
	// AllowedChannels lists the alert channels the plan may configure.
	// Enforced only at configuration time: channels created before a
	// downgrade keep delivering (grace), the user just can't add more.
	AllowedChannels []AlertChannel
}

// Unlimited is the sentinel used for plans with no cap on a given resource.
const Unlimited = -1

// Channel sets per tier. Kept in sync with the pricing page copy
// (upguardly-client app/i18n/locales/*.json, pricing.*.features.integrations).
var (
	freeChannels = []AlertChannel{AlertChannelEMAIL, AlertChannelSMS, AlertChannelDISCORD}
	paidChannels = []AlertChannel{AlertChannelEMAIL, AlertChannelSMS, AlertChannelDISCORD, AlertChannelSLACK, AlertChannelTELEGRAM}
)

// ChannelAllowed reports whether the plan may configure alerts on the channel.
func (l PlanLimits) ChannelAllowed(ch AlertChannel) bool {
	for _, allowed := range l.AllowedChannels {
		if allowed == ch {
			return true
		}
	}
	return false
}

// LimitsForPlan returns the resource limits for a subscription plan name.
// Unknown or empty plans fall back to the FREE tier.
func LimitsForPlan(plan string) PlanLimits {
	switch plan {
	case "PRO":
		return PlanLimits{MaxMonitors: 20, MaxAlertsPerMonitor: 10, MaxGlobalChannels: 10, MinInterval: 60, AllowedChannels: paidChannels}
	case "ENTERPRISE":
		return PlanLimits{MaxMonitors: 200, MaxAlertsPerMonitor: Unlimited, MaxGlobalChannels: Unlimited, MinInterval: 60, AllowedChannels: paidChannels}
	default: // FREE and anything unrecognised
		return PlanLimits{MaxMonitors: 5, MaxAlertsPerMonitor: 1, MaxGlobalChannels: 1, MinInterval: 300, AllowedChannels: freeChannels}
	}
}
