package models

// PlanLimits describes the per-plan resource caps. A value of -1 means unlimited.
type PlanLimits struct {
	MaxMonitors int
	// MaxGlobalChannels caps the account-level notification channels (the
	// user-facing "integrations"), which are the only alert destinations.
	MaxGlobalChannels int
	// MinInterval is the smallest allowed check interval (in seconds) for the
	// plan. Lower tiers are throttled to longer intervals to bound load.
	// Enforced at configuration time and re-applied to existing monitors
	// whenever the effective plan changes (ReconcileMonitorsToPlan): a
	// scheduled cancellation keeps its paid plan — and its intervals — until
	// Stripe ends the billing period, and only then are monitors clamped.
	MinInterval int
	// AllowedChannels lists the alert channels the plan may configure.
	// Enforced only at configuration time: channels created before a
	// downgrade keep delivering (grace), the user just can't add more.
	AllowedChannels []AlertChannel
	// MaxRegions caps how many regions a single monitor may be checked from.
	// Enforced at configuration time and, like MinInterval, re-applied when
	// the effective plan changes: once a downgrade lands (after the paid
	// period ends), region lists over the cap are trimmed.
	MaxRegions int
	// MaxLoginSeats caps invited (non-owner) organization members; the owner
	// is free. PENDING non-expired invitations count against the cap, so a
	// seat is consumed the moment an invitation goes out. Orgs are an
	// ENTERPRISE feature, so lower tiers carry 0.
	MaxLoginSeats int
	// MaxAlertRecipients caps an organization's notify-only alert recipients
	// (the "alerting seats"): bare EMAIL/SMS contacts that receive alerts for
	// every org monitor. The owner's own channels don't count.
	MaxAlertRecipients int
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

// EffectiveInterval resolves a monitor's stored interval to the value the
// scheduler should actually use. A non-nil raw interval is an explicit override
// and is returned as-is (write-time validation already enforced the plan floor
// and timeout < interval). A nil interval means "follow plan": it resolves to
// the plan's minimum, but never below timeout+1 so the timeout < interval
// invariant survives a plan upgrade that drops the floor beneath an existing
// timeout. KEEP IN SYNC with maintenance.effective_interval (the SQL mirror
// used only for the quorum freshness window).
func EffectiveInterval(raw *int, plan string, timeout int) int {
	if raw != nil {
		return *raw
	}
	floor := LimitsForPlan(plan).MinInterval
	if timeout >= floor {
		floor = timeout + 1
	}
	return floor
}

// LimitsForPlan returns the resource limits for a subscription plan name.
// Unknown or empty plans fall back to the FREE tier.
func LimitsForPlan(plan string) PlanLimits {
	switch plan {
	case "PRO":
		return PlanLimits{MaxMonitors: 20, MaxGlobalChannels: 10, MinInterval: 60, AllowedChannels: paidChannels, MaxRegions: 3}
	case "ENTERPRISE":
		return PlanLimits{MaxMonitors: 200, MaxGlobalChannels: Unlimited, MinInterval: 60, AllowedChannels: paidChannels, MaxRegions: Unlimited, MaxLoginSeats: 3, MaxAlertRecipients: 3}
	default: // FREE and anything unrecognised
		// Integrations are the only alert destinations (per-monitor alerts no
		// longer exist), so FREE gets one per allowed channel type — matching
		// the pricing page's "3 alert integrations".
		return PlanLimits{MaxMonitors: 5, MaxGlobalChannels: 3, MinInterval: 300, AllowedChannels: freeChannels, MaxRegions: 1}
	}
}
