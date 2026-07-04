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
)

type Monitor struct {
	ID        string      `json:"id"`
	OrgID     *string     `json:"orgId,omitempty"`
	Name      string      `json:"name"`
	Type      MonitorType `json:"type"`
	Target    string      `json:"target"`
	Interval  int         `json:"interval"`
	Timeout   int         `json:"timeout"`
	Enabled   bool        `json:"enabled"`
	Regions   []string    `json:"regions"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
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
	// Regions this monitor is checked from. Empty means "not provided": the
	// handler defaults it to the default region.
	Regions []string `json:"regions"`
}

type UpdateMonitorRequest struct {
	Name     *string      `json:"name"`
	Type     *MonitorType `json:"type" binding:"omitempty,oneof=HTTP PORT PING"`
	Target   *string      `json:"target"`
	Interval *int         `json:"interval"`
	Timeout  *int         `json:"timeout"`
	Enabled  *bool        `json:"enabled"`
	Regions  *[]string    `json:"regions"`
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
	if r.Interval != nil && (*r.Interval < MonitorIntervalMin || *r.Interval > MonitorIntervalMax) {
		return fmt.Errorf("interval must be between %d and %d seconds", MonitorIntervalMin, MonitorIntervalMax)
	}
	if r.Timeout != nil && (*r.Timeout < MonitorTimeoutMin || *r.Timeout > MonitorTimeoutMax) {
		return fmt.Errorf("timeout must be between %d and %d seconds", MonitorTimeoutMin, MonitorTimeoutMax)
	}
	return nil
}
