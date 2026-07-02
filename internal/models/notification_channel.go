package models

import "time"

// NotificationChannel is a user's global (account-level) alert channel. Every
// monitor the user owns (or their org owns, resolved via the org owner)
// inherits it by default; a MonitorChannelSetting row overrides the enabled
// flag for one monitor.
type NotificationChannel struct {
	ID        string       `json:"id"`
	Channel   AlertChannel `json:"channel"`
	Target    string       `json:"target"`
	Enabled   bool         `json:"enabled"`
	CreatedAt time.Time    `json:"createdAt"`
}

type CreateNotificationChannelRequest struct {
	Channel AlertChannel `json:"channel" binding:"required,oneof=EMAIL SMS DISCORD SLACK TELEGRAM"`
	Target  string       `json:"target" binding:"required"`
	Enabled *bool        `json:"enabled"`
}

func (r *CreateNotificationChannelRequest) SetDefaults() {
	if r.Enabled == nil {
		enabled := true
		r.Enabled = &enabled
	}
}

type UpdateNotificationChannelRequest struct {
	Channel *AlertChannel `json:"channel" binding:"omitempty,oneof=EMAIL SMS DISCORD SLACK TELEGRAM"`
	Target  *string       `json:"target"`
	Enabled *bool         `json:"enabled"`
}

// MonitorChannelSetting is a per-monitor override of a global channel's
// enabled flag. No row for a (monitor, channel) pair means the monitor
// inherits the channel's own enabled flag.
type MonitorChannelSetting struct {
	ID                    string `json:"id"`
	MonitorID             string `json:"monitorId"`
	NotificationChannelID string `json:"notificationChannelId"`
	Enabled               bool   `json:"enabled"`
}

// MonitorChannelState is the merged view returned to clients: a global
// channel plus its effective state for one monitor.
type MonitorChannelState struct {
	NotificationChannel
	// Overridden reports whether the monitor has its own setting for this
	// channel (as opposed to inheriting the channel's enabled flag).
	Overridden bool `json:"overridden"`
	// EffectiveEnabled is what the scheduler will actually use for this
	// monitor: the override when present, the global flag otherwise.
	EffectiveEnabled bool `json:"effectiveEnabled"`
}

type SetMonitorChannelRequest struct {
	Enabled *bool `json:"enabled" binding:"required"`
}
