package models

// PlanLimits describes the per-plan resource caps. A value of -1 means unlimited.
type PlanLimits struct {
	MaxMonitors int
	// MaxChannelsOfType caps how many account-level notification channels
	// (the user-facing "integrations") of each kind may be configured, and
	// doubles as the plan's channel allow-list: a channel absent from the map
	// is not available on the plan at all. EMAIL and SMS address the account
	// holder — one address, one number — so they are capped at one apiece on
	// every plan; the webhook and chat channels address a destination rather
	// than a person, and an account may legitimately want several.
	MaxChannelsOfType map[AlertChannel]int
	// MinInterval is the smallest allowed check interval (in seconds) for the
	// plan. Lower tiers are throttled to longer intervals to bound load.
	// Enforced at configuration time and re-applied to existing monitors
	// whenever the effective plan changes (ReconcileMonitorsToPlan): a
	// scheduled cancellation keeps its paid plan — and its intervals — until
	// Stripe ends the billing period, and only then are monitors clamped.
	MinInterval int
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
	// CustomDegradedThreshold allows overriding the per-type slow-response
	// (DEGRADED) latency threshold on individual monitors. Enforced at
	// configuration time and re-applied on plan changes: overrides are cleared
	// when the effective plan loses the capability.
	CustomDegradedThreshold bool
	// MaintenanceWindows allows configuring per-monitor alert-suppression
	// windows. Enforced at configuration time only: existing windows keep
	// suppressing after a downgrade (grace), the user just can't add more.
	MaintenanceWindows bool
	// MaxAlertRepeats caps a monitor's repeat_alert_max_count (0 = the repeat-
	// alerts feature is unavailable). Enforced at configuration time and
	// re-applied on plan changes: repeat config is cleared when the effective
	// plan loses the capability.
	MaxAlertRepeats int
	// SSLMonitoring / DomainMonitoring allow the certificate- and domain-
	// expiry sub-checks on HTTP monitors. Enforced at configuration time and
	// re-applied on plan changes: the flags are cleared when the effective
	// plan loses the capability.
	SSLMonitoring    bool
	DomainMonitoring bool
}

// Unlimited is the sentinel used for plans with no cap on a given resource.
const Unlimited = -1

// Channel caps per tier. Enforced only at configuration time: channels
// configured before a downgrade keep delivering (grace), the user just can't
// add more. Kept in sync with the pricing page copy (upguardly-client
// app/i18n/locales/*.json, pricing.*.features.integrations) and with
// channelLimitsForPlan (upguardly-client app/dashboard/types.ts).
var (
	freeChannelCaps = map[AlertChannel]int{
		AlertChannelEMAIL:   1,
		AlertChannelSMS:     1,
		AlertChannelDISCORD: 2,
	}
	proChannelCaps = map[AlertChannel]int{
		AlertChannelEMAIL:    1,
		AlertChannelSMS:      1,
		AlertChannelDISCORD:  5,
		AlertChannelSLACK:    5,
		AlertChannelTELEGRAM: 5,
	}
	enterpriseChannelCaps = map[AlertChannel]int{
		AlertChannelEMAIL:    1,
		AlertChannelSMS:      1,
		AlertChannelDISCORD:  Unlimited,
		AlertChannelSLACK:    Unlimited,
		AlertChannelTELEGRAM: Unlimited,
	}
)

// ChannelAllowed reports whether the plan may configure alerts on the channel.
func (l PlanLimits) ChannelAllowed(ch AlertChannel) bool {
	_, ok := l.MaxChannelsOfType[ch]
	return ok
}

// MaxChannelsFor returns how many integrations of ch the plan allows (possibly
// Unlimited) and whether it allows ch at all.
func (l PlanLimits) MaxChannelsFor(ch AlertChannel) (int, bool) {
	max, ok := l.MaxChannelsOfType[ch]
	return max, ok
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
		return PlanLimits{MaxMonitors: 20, MaxChannelsOfType: proChannelCaps, MinInterval: 60, MaxRegions: 3, CustomDegradedThreshold: true}
	case "ENTERPRISE":
		return PlanLimits{MaxMonitors: 200, MaxChannelsOfType: enterpriseChannelCaps, MinInterval: 60, MaxRegions: Unlimited, MaxLoginSeats: 3, MaxAlertRecipients: 3, CustomDegradedThreshold: true, MaintenanceWindows: true, MaxAlertRepeats: 10, SSLMonitoring: true, DomainMonitoring: true}
	default: // FREE and anything unrecognised
		// Integrations are the only alert destinations (per-monitor alerts no
		// longer exist): FREE gets the account's own email and number plus a
		// couple of Discord webhooks, so a monitor can have its own
		// destination without paying.
		return PlanLimits{MaxMonitors: 5, MaxChannelsOfType: freeChannelCaps, MinInterval: 300, MaxRegions: 1}
	}
}
