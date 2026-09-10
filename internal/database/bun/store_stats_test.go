package bun

import (
	"testing"
	"time"
)

func TestMeanTimeBetweenFailures(t *testing.T) {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }
	ptr := func(tm time.Time) *time.Time { return &tm }

	cases := []struct {
		name      string
		incidents []Incident
		want      *int64
	}{
		{name: "no incidents", incidents: nil, want: nil},
		{
			name:      "single incident",
			incidents: []Incident{{StartedAt: at(0), ResolvedAt: ptr(at(5))}},
			want:      nil,
		},
		{
			name: "two resolved incidents",
			incidents: []Incident{
				{StartedAt: at(0), ResolvedAt: ptr(at(5))},
				{StartedAt: at(35)}, // 30m after previous resolution
			},
			want: int64Ptr(1800),
		},
		{
			name: "averages multiple gaps",
			incidents: []Incident{
				{StartedAt: at(0), ResolvedAt: ptr(at(5))},
				{StartedAt: at(15), ResolvedAt: ptr(at(20))}, // gap 10m
				{StartedAt: at(50)},                          // gap 30m
			},
			want: int64Ptr(20 * 60),
		},
		{
			name: "open previous incident contributes no gap",
			incidents: []Incident{
				{StartedAt: at(0)}, // never resolved
				{StartedAt: at(60)},
			},
			want: nil,
		},
		{
			name: "negative gap clamps to zero",
			incidents: []Incident{
				{StartedAt: at(0), ResolvedAt: ptr(at(10))},
				{StartedAt: at(5)}, // overlaps previous resolution
			},
			want: int64Ptr(0),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := meanTimeBetweenFailures(tc.incidents)
			switch {
			case got == nil && tc.want == nil:
			case got == nil || tc.want == nil:
				t.Fatalf("got %v, want %v", fmtPtr(got), fmtPtr(tc.want))
			case *got != *tc.want:
				t.Fatalf("got %d, want %d", *got, *tc.want)
			}
		})
	}
}

func int64Ptr(v int64) *int64 { return &v }

func fmtPtr(p *int64) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

// The 24h stats path reads raw rows. A DOWN check's latency is the timeout the
// checker waited out — a rebooting ping target records the full 30s — so it must
// stay out of min/avg/max while still counting as a check.
func TestComputeStatsExcludesDownLatency(t *testing.T) {
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)
	at := func(minutes int) time.Time { return since.Add(time.Duration(minutes) * time.Minute) }

	rs := []MonitorResult{
		{Status: "UP", Latency: 40, CheckedAt: at(0)},
		{Status: "DOWN", Latency: 30000, CheckedAt: at(10)}, // ping timeout during a reboot
		{Status: "DEGRADED", Latency: 900, CheckedAt: at(20)},
		{Status: "UP", Latency: 60, CheckedAt: at(30)},
	}

	stats := computeStats(rs, since, until)

	if stats.TotalChecks != 4 {
		t.Errorf("TotalChecks: got %d, want 4 (the DOWN check still happened)", stats.TotalChecks)
	}
	if stats.MinLatency != 40 || stats.MaxLatency != 900 {
		t.Errorf("latency range: got [%d,%d], want [40,900]", stats.MinLatency, stats.MaxLatency)
	}
	if want := 1000.0 / 3.0; stats.AvgLatency != want {
		t.Errorf("AvgLatency: got %v, want %v (3 responsive samples)", stats.AvgLatency, want)
	}
	for _, p := range stats.Points {
		if p.AvgLatency > 900 {
			t.Errorf("bucket avg %v includes the timeout", p.AvgLatency)
		}
	}
}

// A window with nothing but failures has no response time to report.
func TestComputeStatsAllDown(t *testing.T) {
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)

	rs := []MonitorResult{
		{Status: "DOWN", Latency: 30000, CheckedAt: since.Add(time.Minute)},
		{Status: "DOWN", Latency: 30000, CheckedAt: since.Add(2 * time.Minute)},
	}

	stats := computeStats(rs, since, until)

	if stats.TotalChecks != 2 {
		t.Errorf("TotalChecks: got %d, want 2", stats.TotalChecks)
	}
	if stats.MinLatency != 0 || stats.MaxLatency != 0 || stats.AvgLatency != 0 {
		t.Errorf("all-down window should report no latency, got %+v", stats)
	}
	if len(stats.Points) != 0 {
		t.Errorf("got %d points, want 0", len(stats.Points))
	}
}
