package models

import (
	"fmt"
	"time"
)

type MonitorType string

const (
	MonitorTypeHTTP MonitorType = "HTTP"
	MonitorTypePORT MonitorType = "PORT"
	MonitorTypePING MonitorType = "PING"
)

// Bounds for monitor configuration.
const (
	MonitorNameMaxLen   = 255
	MonitorTargetMaxLen = 2048
	MonitorIntervalMin  = 60    // 1 minute
	MonitorIntervalMax  = 86400 // 24 hours
	MonitorTimeoutMin   = 5
	MonitorTimeoutMax   = 300 // 5 minutes

	DegradedThresholdMinMs = 100
	DegradedThresholdMaxMs = 60000

	// Repeat alerts: minimum spacing between reminders and a static ceiling on
	// the count (the effective ceiling is the plan's MaxAlertRepeats).
	RepeatAlertIntervalMinSecs = 300
	RepeatAlertIntervalMaxSecs = 86400
)

// DefaultDegradedThresholdMs is the slow-response threshold applied when a
// monitor has no explicit override: an UP check slower than this is
// classified DEGRADED.
func DefaultDegradedThresholdMs(t MonitorType) int {
	switch t {
	case MonitorTypePORT:
		return 1000
	case MonitorTypePING:
		return 500
	default: // HTTP and anything unrecognised
		return 2000
	}
}

// EffectiveDegradedThreshold resolves a monitor's stored threshold (NULL =
// follow the per-type default) to the value the scheduler should apply.
func EffectiveDegradedThreshold(raw *int, t MonitorType) int {
	if raw != nil {
		return *raw
	}
	return DefaultDegradedThresholdMs(t)
}

type Monitor struct {
	ID    string  `json:"id"`
	OrgID *string `json:"orgId,omitempty"`
	// Plan is the effective plan governing this monitor: the org owner's for an
	// org monitor, the creator's for a solo one. Clients gate per-monitor
	// features on it rather than on the viewer's own subscription, which for an
	// invited org member is not the plan the monitor runs on.
	Plan   string      `json:"plan"`
	Name   string      `json:"name"`
	Type   MonitorType `json:"type"`
	Target string      `json:"target"`
	// Interval is the effective check interval in seconds — the plan minimum
	// for follow-plan monitors, or the explicit override. IntervalIsCustom is
	// false when the monitor follows its plan (stored interval is NULL).
	Interval         int  `json:"interval"`
	IntervalIsCustom bool `json:"intervalIsCustom"`
	Timeout          int  `json:"timeout"`
	Enabled          bool `json:"enabled"`
	// DegradedThresholdMs is the effective slow-response threshold in
	// milliseconds — the per-type default, or the explicit override.
	// DegradedThresholdIsCustom is false when the monitor follows the default
	// (stored threshold is NULL).
	DegradedThresholdMs       int  `json:"degradedThresholdMs"`
	DegradedThresholdIsCustom bool `json:"degradedThresholdIsCustom"`
	// Repeat alerts (ENTERPRISE): while an incident stays open, re-send its
	// alert every RepeatAlertIntervalSecs, at most RepeatAlertMaxCount times.
	// Both nil = feature off; always set or cleared together.
	RepeatAlertIntervalSecs *int `json:"repeatAlertIntervalSecs,omitempty"`
	RepeatAlertMaxCount     *int `json:"repeatAlertMaxCount,omitempty"`
	// Expiry monitoring (ENTERPRISE, HTTP monitors only): alert when the TLS
	// certificate / registered domain approaches expiry. Configured via
	// UpdateMonitor; state served by GET /v1/monitors/:id/expiry.
	CertCheckEnabled          bool      `json:"certCheckEnabled"`
	CertExpiryThresholdDays   int       `json:"certExpiryThresholdDays"`
	DomainCheckEnabled        bool      `json:"domainCheckEnabled"`
	DomainExpiryThresholdDays int       `json:"domainExpiryThresholdDays"`
	Regions                   []string  `json:"regions"`
	CreatedAt                 time.Time `json:"createdAt"`
	UpdatedAt                 time.Time `json:"updatedAt"`
}

type CreateMonitorRequest struct {
	// OrgID is optional: empty means a solo (FREE/PRO) monitor owned directly by
	// the user; a value means the monitor belongs to that organization.
	OrgID    string      `json:"orgId"`
	Name     string      `json:"name" binding:"required"`
	Type     MonitorType `json:"type" binding:"required,oneof=HTTP PORT PING"`
	Target   string      `json:"target" binding:"required"`
	Interval int         `json:"interval"`
	Timeout  int         `json:"timeout"`
	Enabled  *bool       `json:"enabled"`
	// DegradedThresholdMs: nil or 0 means follow the per-type default (stores
	// NULL); an explicit value is a plan-gated override (PRO/ENTERPRISE).
	DegradedThresholdMs *int `json:"degradedThresholdMs"`
	// Repeat alerts (ENTERPRISE): nil/0 = off; both must be set together.
	RepeatAlertIntervalSecs *int `json:"repeatAlertIntervalSecs"`
	RepeatAlertMaxCount     *int `json:"repeatAlertMaxCount"`
	// Expiry monitoring (ENTERPRISE, HTTP only): optional at creation. The
	// handler applies these via a follow-up UpdateMonitor call so CreateMonitor
	// itself doesn't need to know about them.
	CertCheckEnabled          *bool `json:"certCheckEnabled"`
	CertExpiryThresholdDays   *int  `json:"certExpiryThresholdDays"`
	DomainCheckEnabled        *bool `json:"domainCheckEnabled"`
	DomainExpiryThresholdDays *int  `json:"domainExpiryThresholdDays"`
	// Regions this monitor is checked from. Empty means "not provided": the
	// handler defaults it to the default region.
	Regions []string `json:"regions"`
}

// CreateMonitorParams is the store-level shape of a monitor insert. It carries
// the resolved billing context alongside the monitor's own fields so the store
// can enforce the quota in the same transaction as the insert: BillingOwnerID
// is whoever's subscription governs the workspace (the caller for a personal
// monitor, the org owner for an org one) and MaxMonitors is that plan's cap.
type CreateMonitorParams struct {
	// UserID is the creator. It is the owner of a personal monitor
	// (OrgID empty); on an org monitor it records who created it and carries
	// no standing of its own.
	UserID string
	OrgID  string

	// BillingOwnerID is the user whose monitor pool this create consumes, and
	// MaxMonitors is that pool's cap — Unlimited disables the check. The two
	// are always resolved together; see handlers.billingOwner.
	BillingOwnerID string
	MaxMonitors    int

	Name   string
	Type   string
	Target string
	// Interval is nil for a follow-plan monitor (resolved to the plan minimum
	// at read time), or an explicit override in seconds.
	Interval *int
	Timeout  int
	// DegradedThresholdMs is nil for the per-type default slow-response
	// threshold, or a plan-gated explicit override in milliseconds.
	DegradedThresholdMs *int
	// RepeatAlertIntervalSecs/RepeatAlertMaxCount enable repeat alerts
	// (ENTERPRISE); both nil = off, always set together.
	RepeatAlertIntervalSecs *int
	RepeatAlertMaxCount     *int
	Enabled                 bool
	Regions                 []string
}

type UpdateMonitorRequest struct {
	Name   *string      `json:"name"`
	Type   *MonitorType `json:"type" binding:"omitempty,oneof=HTTP PORT PING"`
	Target *string      `json:"target"`
	// Interval: a positive value is an explicit override (must be >= the plan
	// minimum); 0 is the "revert to follow-plan" sentinel (stores NULL); nil
	// leaves the interval unchanged.
	Interval *int  `json:"interval"`
	Timeout  *int  `json:"timeout"`
	Enabled  *bool `json:"enabled"`
	// DegradedThresholdMs: a positive value is an explicit override (plan-
	// gated); 0 is the "revert to per-type default" sentinel (stores NULL);
	// nil leaves the threshold unchanged.
	DegradedThresholdMs *int `json:"degradedThresholdMs"`
	// Repeat alerts: when changing, send both — positive values enable
	// (plan-gated), 0 on either disables (both store NULL); nil leaves them
	// unchanged.
	RepeatAlertIntervalSecs *int `json:"repeatAlertIntervalSecs"`
	RepeatAlertMaxCount     *int `json:"repeatAlertMaxCount"`
	// Expiry monitoring (ENTERPRISE, HTTP only): enabling is plan-gated;
	// disabling is always allowed. Thresholds are days before expiry (1-90).
	CertCheckEnabled          *bool     `json:"certCheckEnabled"`
	CertExpiryThresholdDays   *int      `json:"certExpiryThresholdDays"`
	DomainCheckEnabled        *bool     `json:"domainCheckEnabled"`
	DomainExpiryThresholdDays *int      `json:"domainExpiryThresholdDays"`
	Regions                   *[]string `json:"regions"`
}

// NormalizeRegions dedupes a region list (preserving order) and rejects ids
// missing from the registry or an effectively empty list. Availability (is a
// pool actually deployed?) is deployment config, checked in the handler.
func NormalizeRegions(regions []string) ([]string, error) {
	out := make([]string, 0, len(regions))
	seen := make(map[string]bool, len(regions))
	for _, r := range regions {
		if seen[r] {
			continue
		}
		if !ValidRegion(r) {
			return nil, fmt.Errorf("unknown region %q", r)
		}
		seen[r] = true
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one region is required")
	}
	return out, nil
}

// SetDefaults fills plan-independent defaults. Interval is deliberately left
// at 0 ("not provided"): its default is the plan's minimum interval, which the
// handler applies after resolving the plan — a flat default like 60 would be
// rejected outright on plans whose minimum is higher (FREE requires 300).
func (r *CreateMonitorRequest) SetDefaults() {
	if r.Timeout == 0 {
		r.Timeout = 30
	}
	if r.Enabled == nil {
		enabled := true
		r.Enabled = &enabled
	}
}

// Validate checks field lengths and interval/timeout bounds. Interval 0 means
// "not provided" and is skipped here: the handler defaults it to the plan's
// minimum (and re-checks timeout against it) after resolving the plan.
func (r *CreateMonitorRequest) Validate() error {
	if len(r.Name) > MonitorNameMaxLen {
		return fmt.Errorf("name must not exceed %d characters", MonitorNameMaxLen)
	}
	if len(r.Target) > MonitorTargetMaxLen {
		return fmt.Errorf("target must not exceed %d characters", MonitorTargetMaxLen)
	}
	if r.Interval != 0 && (r.Interval < MonitorIntervalMin || r.Interval > MonitorIntervalMax) {
		return fmt.Errorf("interval must be between %d and %d seconds", MonitorIntervalMin, MonitorIntervalMax)
	}
	if r.Timeout < MonitorTimeoutMin || r.Timeout > MonitorTimeoutMax {
		return fmt.Errorf("timeout must be between %d and %d seconds", MonitorTimeoutMin, MonitorTimeoutMax)
	}
	if r.Interval != 0 && r.Timeout >= r.Interval {
		return fmt.Errorf("timeout (%ds) must be less than interval (%ds)", r.Timeout, r.Interval)
	}
	if err := validateDegradedThreshold(r.DegradedThresholdMs); err != nil {
		return err
	}
	if err := validateRepeatAlerts(r.RepeatAlertIntervalSecs, r.RepeatAlertMaxCount); err != nil {
		return err
	}
	for _, threshold := range []*int{r.CertExpiryThresholdDays, r.DomainExpiryThresholdDays} {
		if threshold != nil && (*threshold < ExpiryThresholdMinDays || *threshold > ExpiryThresholdMaxDays) {
			return fmt.Errorf("expiry threshold must be between %d and %d days", ExpiryThresholdMinDays, ExpiryThresholdMaxDays)
		}
	}
	return nil
}

// validateDegradedThreshold bounds an explicit slow-response threshold. nil
// and 0 both mean "follow the per-type default" and are skipped.
func validateDegradedThreshold(v *int) error {
	if v == nil || *v == 0 {
		return nil
	}
	if *v < DegradedThresholdMinMs || *v > DegradedThresholdMaxMs {
		return fmt.Errorf("degraded threshold must be between %d and %d milliseconds", DegradedThresholdMinMs, DegradedThresholdMaxMs)
	}
	return nil
}

// validateRepeatAlerts checks the repeat-alert pair: both absent (nil/0) is
// off, both positive enables (bounds-checked; the plan's count ceiling is
// applied in the handler), anything mixed is an error.
func validateRepeatAlerts(interval, count *int) error {
	iv := 0
	if interval != nil {
		iv = *interval
	}
	cv := 0
	if count != nil {
		cv = *count
	}
	if iv == 0 && cv == 0 {
		return nil
	}
	if iv == 0 || cv == 0 {
		return fmt.Errorf("repeatAlertIntervalSecs and repeatAlertMaxCount must be set together")
	}
	if iv < RepeatAlertIntervalMinSecs || iv > RepeatAlertIntervalMaxSecs {
		return fmt.Errorf("repeat alert interval must be between %d and %d seconds", RepeatAlertIntervalMinSecs, RepeatAlertIntervalMaxSecs)
	}
	if cv < 1 {
		return fmt.Errorf("repeat alert count must be at least 1")
	}
	return nil
}

// ValidateUpdate checks only the fields present in an update request.
func (r *UpdateMonitorRequest) Validate() error {
	if r.Name != nil && len(*r.Name) > MonitorNameMaxLen {
		return fmt.Errorf("name must not exceed %d characters", MonitorNameMaxLen)
	}
	if r.Target != nil && len(*r.Target) > MonitorTargetMaxLen {
		return fmt.Errorf("target must not exceed %d characters", MonitorTargetMaxLen)
	}
	// 0 is the "revert to follow-plan" sentinel; only bound explicit values.
	if r.Interval != nil && *r.Interval != 0 && (*r.Interval < MonitorIntervalMin || *r.Interval > MonitorIntervalMax) {
		return fmt.Errorf("interval must be between %d and %d seconds", MonitorIntervalMin, MonitorIntervalMax)
	}
	if r.Timeout != nil && (*r.Timeout < MonitorTimeoutMin || *r.Timeout > MonitorTimeoutMax) {
		return fmt.Errorf("timeout must be between %d and %d seconds", MonitorTimeoutMin, MonitorTimeoutMax)
	}
	if err := validateDegradedThreshold(r.DegradedThresholdMs); err != nil {
		return err
	}
	for _, threshold := range []*int{r.CertExpiryThresholdDays, r.DomainExpiryThresholdDays} {
		if threshold != nil && (*threshold < ExpiryThresholdMinDays || *threshold > ExpiryThresholdMaxDays) {
			return fmt.Errorf("expiry threshold must be between %d and %d days", ExpiryThresholdMinDays, ExpiryThresholdMaxDays)
		}
	}
	// Only validate the repeat pair when the request touches it (both nil =
	// unchanged). A 0 on either side is the "disable" sentinel.
	if r.RepeatAlertIntervalSecs != nil || r.RepeatAlertMaxCount != nil {
		iv, cv := 0, 0
		if r.RepeatAlertIntervalSecs != nil {
			iv = *r.RepeatAlertIntervalSecs
		}
		if r.RepeatAlertMaxCount != nil {
			cv = *r.RepeatAlertMaxCount
		}
		if iv != 0 && cv != 0 {
			if err := validateRepeatAlerts(&iv, &cv); err != nil {
				return err
			}
		}
	}
	return nil
}
