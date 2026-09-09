package models

import (
	"testing"
	"time"
)

func TestExpiryDaysLeft(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name      string
		expiresAt time.Time
		want      int
	}{
		{"far future", now.Add(30 * 24 * time.Hour), 30},
		{"just under a day", now.Add(23 * time.Hour), 0},
		{"exactly now", now, 0},
		{"expired one hour ago", now.Add(-time.Hour), -1},
		{"expired 25 hours ago", now.Add(-25 * time.Hour), -2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpiryDaysLeft(tc.expiresAt, now)
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestExpiryAlertBucket(t *testing.T) {
	cases := []struct {
		name          string
		daysLeft      int
		thresholdDays int
		want          *int
	}{
		{"far from expiry, no alert", 30, 14, nil},
		{"expired", -1, 14, intPtr(0)},
		{"exactly at 1 day", 1, 14, intPtr(1)},
		{"under 1 day rounds into the 1-day bucket", 0, 14, intPtr(1)},
		{"at 3 days", 3, 14, intPtr(3)},
		{"between 3 and threshold", 10, 14, intPtr(14)},
		{"at threshold", 14, 14, intPtr(14)},
		{"just past threshold, no alert", 15, 14, nil},
		{"threshold below 3 skips the 3-day bucket", 2, 2, intPtr(2)},
		{"threshold below 1 still catches expired", -1, 0, intPtr(0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpiryAlertBucket(tc.daysLeft, tc.thresholdDays)
			switch {
			case got == nil && tc.want == nil:
			case got == nil || tc.want == nil:
				t.Fatalf("got %v, want %v", got, tc.want)
			case *got != *tc.want:
				t.Fatalf("got bucket %d, want %d", *got, *tc.want)
			}
		})
	}
}

func intPtr(v int) *int { return &v }
