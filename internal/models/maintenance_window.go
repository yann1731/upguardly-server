package models

import (
	"fmt"
	"time"
)

// MaintenanceWindowKind distinguishes a concrete one-off range from a weekly
// recurrence.
type MaintenanceWindowKind string

const (
	MaintenanceWindowOneOff MaintenanceWindowKind = "ONE_OFF"
	MaintenanceWindowWeekly MaintenanceWindowKind = "WEEKLY"
)

// MaintenanceWindow is a per-monitor window during which no alerts are
// emitted (checks keep running and incidents keep opening/resolving; only
// notification emission is suppressed). ENTERPRISE-only
// (PlanLimits.MaintenanceWindows). ONE_OFF rows carry StartsAt/EndsAt; WEEKLY
// rows carry Weekday/StartTime/DurationMinutes/Timezone.
type MaintenanceWindow struct {
	ID        string                `json:"id"`
	MonitorID string                `json:"monitorId"`
	Kind      MaintenanceWindowKind `json:"kind"`
	StartsAt  *time.Time            `json:"startsAt,omitempty"`
	EndsAt    *time.Time            `json:"endsAt,omitempty"`
	// Weekday follows Postgres EXTRACT(dow): 0=Sunday .. 6=Saturday.
	Weekday *int `json:"weekday,omitempty"`
	// StartTime is the local wall-clock start, "HH:MM" (seconds accepted and
	// truncated).
	StartTime       *string `json:"startTime,omitempty"`
	DurationMinutes *int    `json:"durationMinutes,omitempty"`
	// Timezone is the IANA zone the weekly occurrence is evaluated in.
	Timezone  *string   `json:"timezone,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// CreateMaintenanceWindowRequest carries either a one-off range or a weekly
// recurrence, matching the window kind.
type CreateMaintenanceWindowRequest struct {
	Kind            MaintenanceWindowKind `json:"kind" binding:"required,oneof=ONE_OFF WEEKLY"`
	StartsAt        *time.Time            `json:"startsAt"`
	EndsAt          *time.Time            `json:"endsAt"`
	Weekday         *int                  `json:"weekday"`
	StartTime       *string               `json:"startTime"`
	DurationMinutes *int                  `json:"durationMinutes"`
	Timezone        *string               `json:"timezone"`
}

// Validate checks the kind-specific field shape. Timezone existence is
// deployment-dependent (time.LoadLocation) and checked in the handler.
func (r *CreateMaintenanceWindowRequest) Validate() error {
	switch r.Kind {
	case MaintenanceWindowOneOff:
		if r.StartsAt == nil || r.EndsAt == nil {
			return fmt.Errorf("one-off windows require startsAt and endsAt")
		}
		if !r.EndsAt.After(*r.StartsAt) {
			return fmt.Errorf("endsAt must be after startsAt")
		}
		if r.Weekday != nil || r.StartTime != nil || r.DurationMinutes != nil || r.Timezone != nil {
			return fmt.Errorf("one-off windows must not carry weekly fields")
		}
	case MaintenanceWindowWeekly:
		if r.Weekday == nil || r.StartTime == nil || r.DurationMinutes == nil || r.Timezone == nil {
			return fmt.Errorf("weekly windows require weekday, startTime, durationMinutes and timezone")
		}
		if *r.Weekday < 0 || *r.Weekday > 6 {
			return fmt.Errorf("weekday must be between 0 (Sunday) and 6 (Saturday)")
		}
		if _, err := time.Parse("15:04", *r.StartTime); err != nil {
			if _, err := time.Parse("15:04:05", *r.StartTime); err != nil {
				return fmt.Errorf("startTime must be HH:MM")
			}
		}
		if *r.DurationMinutes < 1 || *r.DurationMinutes > 1440 {
			return fmt.Errorf("durationMinutes must be between 1 and 1440")
		}
		if r.StartsAt != nil || r.EndsAt != nil {
			return fmt.Errorf("weekly windows must not carry one-off fields")
		}
	default:
		return fmt.Errorf("unknown window kind %q", r.Kind)
	}
	return nil
}
