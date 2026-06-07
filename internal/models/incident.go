package models

import "time"

// Incident represents a contiguous period during which a monitor was unhealthy
// (DOWN or DEGRADED). It opens on the first unhealthy check and resolves when
// the monitor recovers.
type Incident struct {
	ID         string     `json:"id"`
	MonitorID  string     `json:"monitorId"`
	Status     Status     `json:"status"`
	StartedAt  time.Time  `json:"startedAt"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
	StatusCode *int       `json:"statusCode,omitempty"`
	Message    *string    `json:"message,omitempty"`
	// DurationMs is the resolved duration in milliseconds, or nil while ongoing.
	DurationMs *int64 `json:"durationMs,omitempty"`
}

// MonitorStats holds aggregate latency statistics and an incident count over a
// time window, plus a bucketed series for graphing average response time.
type MonitorStats struct {
	MinLatency    int         `json:"minLatency"`
	MaxLatency    int         `json:"maxLatency"`
	AvgLatency    float64     `json:"avgLatency"`
	TotalChecks   int         `json:"totalChecks"`
	IncidentCount int         `json:"incidentCount"`
	Points        []StatPoint `json:"points"`
}

// StatPoint is a single time bucket of average response time.
type StatPoint struct {
	Timestamp  time.Time `json:"timestamp"`
	AvgLatency float64   `json:"avgLatency"`
}
