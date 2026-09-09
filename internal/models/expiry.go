package models

import "time"

// ExpiryKind distinguishes the two expiry sub-checks of an HTTP monitor.
type ExpiryKind string

const (
	ExpiryKindCert   ExpiryKind = "CERT"
	ExpiryKindDomain ExpiryKind = "DOMAIN"
)

// Bounds for the per-monitor expiry alert thresholds (days before expiry).
const (
	ExpiryThresholdMinDays = 1
	ExpiryThresholdMaxDays = 90
)

// ExpiryCheckClaim is one due sub-check handed to the scheduler's expiry
// sweep by ClaimDueExpiryChecks (claiming advances checked_at, so concurrent
// instances never double-check).
type ExpiryCheckClaim struct {
	MonitorID     string
	Kind          ExpiryKind
	Target        string
	ThresholdDays int
}

// MonitorExpiryStatus is the API view of one expiry sub-check's state
// (GET /v1/monitors/:id/expiry).
type MonitorExpiryStatus struct {
	Kind      ExpiryKind `json:"kind"`
	ExpiresAt *time.Time `json:"expiresAt"`
	// DaysLeft is derived from ExpiresAt at read time; nil while unknown.
	DaysLeft  *int      `json:"daysLeft"`
	CheckedAt time.Time `json:"checkedAt"`
	LastError *string   `json:"lastError"`
}

// ExpiryAlertBucket returns the alert bucket the expiry currently sits in:
// 0 = expired, then escalation points at 1 day, 3 days, and the configured
// threshold (buckets above the threshold don't exist — a threshold of 2
// yields buckets 2/1/0). nil = outside every bucket, nothing to alert.
// An alert fires on the first crossing of each bucket per expires_at value
// (tracked via last_alert_bucket + alerted_expires_at; a renewal changes
// expires_at and re-arms all buckets).
func ExpiryAlertBucket(daysLeft, thresholdDays int) *int {
	bucketOf := func(b int) *int { return &b }
	switch {
	case daysLeft < 0:
		return bucketOf(0)
	case daysLeft <= 1 && thresholdDays >= 1:
		return bucketOf(1)
	case daysLeft <= 3 && thresholdDays >= 3:
		return bucketOf(3)
	case daysLeft <= thresholdDays:
		return bucketOf(thresholdDays)
	default:
		return nil
	}
}

// ExpiryDaysLeft floors the time remaining until expiry to whole days
// (negative once expired).
func ExpiryDaysLeft(expiresAt, now time.Time) int {
	hours := expiresAt.Sub(now).Hours()
	days := int(hours / 24)
	if hours < 0 && hours/24 != float64(days) {
		days-- // floor toward negative infinity so "expired 1h ago" is < 0
	}
	return days
}
