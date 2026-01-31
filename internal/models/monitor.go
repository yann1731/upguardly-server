package models

import "time"

type MonitorType string

const (
	MonitorTypeHTTP MonitorType = "HTTP"
	MonitorTypePORT MonitorType = "PORT"
	MonitorTypePING MonitorType = "PING"
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
