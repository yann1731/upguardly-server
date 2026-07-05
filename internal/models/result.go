package models

import "time"

type Status string

const (
	StatusUP       Status = "UP"
	StatusDOWN     Status = "DOWN"
	StatusDEGRADED Status = "DEGRADED"
)

type MonitorResult struct {
	ID         string    `json:"id"`
	MonitorID  string    `json:"monitorId"`
	Status     Status    `json:"status"`
	Latency    int       `json:"latency"`
	StatusCode *int      `json:"statusCode,omitempty"`
	Message    *string   `json:"message,omitempty"`
	Region     string    `json:"region"`
	CheckedAt  time.Time `json:"checkedAt"`
}

type AlertHistory struct {
	ID      string    `json:"id"`
	AlertID string    `json:"alertId"`
	Status  Status    `json:"status"`
	Message string    `json:"message"`
	SentAt  time.Time `json:"sentAt"`
}

type CheckResult struct {
	Status     Status
	Latency    int
	StatusCode *int
	Message    string
}

type PendingResult struct {
	MonitorID string
	Result    CheckResult
}
