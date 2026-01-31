package models

import "time"

type AlertChannel string

const (
	AlertChannelEMAIL   AlertChannel = "EMAIL"
	AlertChannelSMS     AlertChannel = "SMS"
	AlertChannelDISCORD AlertChannel = "DISCORD"
	AlertChannelSLACK   AlertChannel = "SLACK"
)

type Alert struct {
	ID        string       `json:"id"`
	MonitorID string       `json:"monitorId"`
	Channel   AlertChannel `json:"channel"`
	Target    string       `json:"target"`
	Enabled   bool         `json:"enabled"`
	CreatedAt time.Time    `json:"createdAt"`
}

type CreateAlertRequest struct {
	Channel AlertChannel `json:"channel" binding:"required,oneof=EMAIL SMS DISCORD SLACK"`
	Target  string       `json:"target" binding:"required"`
	Enabled *bool        `json:"enabled"`
}

type UpdateAlertRequest struct {
	Channel *AlertChannel `json:"channel" binding:"omitempty,oneof=EMAIL SMS DISCORD SLACK"`
	Target  *string       `json:"target"`
	Enabled *bool         `json:"enabled"`
}

func (r *CreateAlertRequest) SetDefaults() {
	if r.Enabled == nil {
		enabled := true
		r.Enabled = &enabled
	}
}
