package models

// Availability, as shown by the dashboard's per-day strip. Distinct from
// MonitorStats, which is latency-only: these are share-of-checks figures
// derived from the per-status counts on the hourly rollups.

// MaxUptimeDays bounds the uptime window. Rollups are retained 400 days, but
// their per-status counts were backfilled from raw monitor_results, which are
// retained 90 — so anything further back would read as 0% available.
const MaxUptimeDays = 90

// DefaultUptimeDays is the window the dashboard strip requests.
const DefaultUptimeDays = 31

// DailyUptime is one calendar day of the strip. Date is a plain YYYY-MM-DD in
// the *caller's* timezone (the day was bucketed against the offset they sent),
// not a timestamp — re-parsing it in another zone would shift the day.
type DailyUptime struct {
	Date string `json:"date"`
	// Monitored is false when the day recorded no checks at all; the UI paints
	// those neutral rather than as an outage. UptimePercent is nil in step.
	Monitored     bool     `json:"monitored"`
	UptimePercent *float64 `json:"uptimePercent"`
}

// MonitorUptime is the uptime endpoint's response. Days always holds exactly
// the requested number of entries, oldest-first, gaps included.
type MonitorUptime struct {
	Days []DailyUptime `json:"days"`
	// Uptime7d is the trailing 7x24h availability. Check-weighted within each
	// region rather than an average of the daily percentages, so a quiet day
	// doesn't count as much as a busy one.
	//
	// nil when no region reported a single check in those seven days — a
	// brand-new monitor, or one whose checks never ran. Distinct from 0, which
	// means checks ran and every one of them failed. Callers must keep the two
	// apart: rendering nil as 0% makes a monitor created a minute ago look
	// exactly like one that is completely down.
	Uptime7d *float64 `json:"uptime7d"`
}
