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
	Name      string      `json:"name"`
	Type      MonitorType `json:"type"`
	Target    string      `json:"target"`
	Interval  int         `json:"interval"`
	Timeout   int         `json:"timeout"`
	Enabled   bool        `json:"enabled"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
}

type CreateMonitorRequest struct {
	Name     string      `json:"name" binding:"required"`
	Type     MonitorType `json:"type" binding:"required,oneof=HTTP PORT PING"`
	Target   string      `json:"target" binding:"required"`
	Interval int         `json:"interval"`
	Timeout  int         `json:"timeout"`
	Enabled  *bool       `json:"enabled"`
}

type UpdateMonitorRequest struct {
	Name     *string      `json:"name"`
	Type     *MonitorType `json:"type" binding:"omitempty,oneof=HTTP PORT PING"`
	Target   *string      `json:"target"`
	Interval *int         `json:"interval"`
	Timeout  *int         `json:"timeout"`
	Enabled  *bool        `json:"enabled"`
}

func (r *CreateMonitorRequest) SetDefaults() {
	if r.Interval == 0 {
		r.Interval = 60
	}
	if r.Timeout == 0 {
		r.Timeout = 30
	}
	if r.Enabled == nil {
		enabled := true
		r.Enabled = &enabled
	}
}

// Validate checks field lengths and interval/timeout bounds.
func (r *CreateMonitorRequest) Validate() error {
	if len(r.Name) > MonitorNameMaxLen {
		return fmt.Errorf("name must not exceed %d characters", MonitorNameMaxLen)
	}
	if len(r.Target) > MonitorTargetMaxLen {
		return fmt.Errorf("target must not exceed %d characters", MonitorTargetMaxLen)
	}
	if r.Interval < MonitorIntervalMin || r.Interval > MonitorIntervalMax {
		return fmt.Errorf("interval must be between %d and %d seconds", MonitorIntervalMin, MonitorIntervalMax)
	}
	if r.Timeout < MonitorTimeoutMin || r.Timeout > MonitorTimeoutMax {
		return fmt.Errorf("timeout must be between %d and %d seconds", MonitorTimeoutMin, MonitorTimeoutMax)
	}
	if r.Timeout >= r.Interval {
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
