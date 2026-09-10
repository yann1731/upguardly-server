package bun

import (
	"math"
	"testing"
	"time"
)

// computeStatsFromRollups must produce the same scalar aggregates as a direct
// pass over the underlying raw samples — that's the contract that lets the 7d/30d
// stats path read rollups instead of raw rows.
func TestComputeStatsFromRollupsMatchesRawAggregates(t *testing.T) {
	until := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	since := until.Add(-7 * 24 * time.Hour)

	// Synthesize a raw sample every 10 minutes across the window with a varying
	// latency, then derive the reference aggregates directly.
	type sample struct {
		at      time.Time
		latency int
	}
	var samples []sample
	for t := since; t.Before(until); t = t.Add(10 * time.Minute) {
		// Deterministic but non-trivial latency pattern.
		lat := 50 + int(t.Unix()/600)%400
		samples = append(samples, sample{at: t, latency: lat})
	}

	refMin, refMax, refSum := samples[0].latency, samples[0].latency, 0
	for _, s := range samples {
		if s.latency < refMin {
			refMin = s.latency
		}
		if s.latency > refMax {
			refMax = s.latency
		}
		refSum += s.latency
	}
	refCount := len(samples)

	// Build hourly rollups from the same samples (what refresh_rollups does).
	type agg struct{ count, sum, min, max int }
	hours := map[time.Time]*agg{}
	for _, s := range samples {
		h := s.at.Truncate(time.Hour)
		a := hours[h]
		if a == nil {
			a = &agg{min: s.latency, max: s.latency}
			hours[h] = a
		}
		a.count++
		a.sum += s.latency
		if s.latency < a.min {
			a.min = s.latency
		}
		if s.latency > a.max {
			a.max = s.latency
		}
	}
	var rows []rollupRow
	for h, a := range hours {
		rows = append(rows, rollupRow{
			Bucket: h, Checks: a.count, LatencyChecks: a.count, SumLatency: a.sum,
			MinLatency: a.min, MaxLatency: a.max,
		})
	}

	stats := computeStatsFromRollups(rows, since, until)

	if stats.MinLatency != refMin {
		t.Errorf("MinLatency: got %d, want %d", stats.MinLatency, refMin)
	}
	if stats.MaxLatency != refMax {
		t.Errorf("MaxLatency: got %d, want %d", stats.MaxLatency, refMax)
	}
	if stats.TotalChecks != refCount {
		t.Errorf("TotalChecks: got %d, want %d", stats.TotalChecks, refCount)
	}
	wantAvg := float64(refSum) / float64(refCount)
	if math.Abs(stats.AvgLatency-wantAvg) > 1e-9 {
		t.Errorf("AvgLatency: got %v, want %v", stats.AvgLatency, wantAvg)
	}

	// The series should be populated and every point's avg within [min,max].
	if len(stats.Points) == 0 {
		t.Fatal("expected a non-empty bucketed series")
	}
	if len(stats.Points) > statBuckets {
		t.Errorf("got %d points, want <= %d", len(stats.Points), statBuckets)
	}
	for _, p := range stats.Points {
		if p.AvgLatency < float64(refMin) || p.AvgLatency > float64(refMax) {
			t.Errorf("point avg %v outside [%d,%d]", p.AvgLatency, refMin, refMax)
		}
	}
}

func TestComputeStatsFromRollupsEmpty(t *testing.T) {
	now := time.Now()
	stats := computeStatsFromRollups(nil, now.Add(-30*24*time.Hour), now)
	if stats.TotalChecks != 0 || stats.MinLatency != 0 || stats.MaxLatency != 0 {
		t.Errorf("empty rollups should yield zero stats, got %+v", stats)
	}
	if stats.Points == nil || len(stats.Points) != 0 {
		t.Errorf("empty rollups should yield empty (non-nil) Points, got %#v", stats.Points)
	}
}

// Per-region stats are the same aggregation run over a region-filtered subset;
// grouping must neither mix regions nor lose rows.
func TestRollupRegionGrouping(t *testing.T) {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	since, until := base, base.Add(48*time.Hour)

	rows := []rollupRow{
		{Region: "ca-east", Bucket: base.Add(1 * time.Hour), Checks: 10, LatencyChecks: 10, SumLatency: 1000, MinLatency: 50, MaxLatency: 200},
		{Region: "eu-west-fr", Bucket: base.Add(1 * time.Hour), Checks: 5, LatencyChecks: 5, SumLatency: 1500, MinLatency: 200, MaxLatency: 400},
		{Region: "ca-east", Bucket: base.Add(2 * time.Hour), Checks: 10, LatencyChecks: 10, SumLatency: 2000, MinLatency: 100, MaxLatency: 300},
	}

	regions := rollupRegions(rows)
	if len(regions) != 2 || regions[0] != "ca-east" || regions[1] != "eu-west-fr" {
		t.Fatalf("rollupRegions = %v, want [ca-east eu-west-fr] in first-seen order", regions)
	}

	var usRows, euRows []rollupRow
	for _, r := range rows {
		if r.Region == "ca-east" {
			usRows = append(usRows, r)
		} else {
			euRows = append(euRows, r)
		}
	}

	us := computeStatsFromRollups(usRows, since, until)
	if us.TotalChecks != 20 || us.MinLatency != 50 || us.MaxLatency != 300 {
		t.Fatalf("ca-east stats = %+v, want 20 checks, min 50, max 300", us)
	}
	if want := 3000.0 / 20.0; us.AvgLatency != want {
		t.Fatalf("ca-east avg = %v, want %v", us.AvgLatency, want)
	}

	eu := computeStatsFromRollups(euRows, since, until)
	if eu.TotalChecks != 5 || eu.MinLatency != 200 || eu.MaxLatency != 400 {
		t.Fatalf("eu-west-fr stats = %+v, want 5 checks, min 200, max 400", eu)
	}
	if want := 1500.0 / 5.0; eu.AvgLatency != want {
		t.Fatalf("eu-west-fr avg = %v, want %v", eu.AvgLatency, want)
	}

	// The overall pass over all rows must equal the union of the groups.
	all := computeStatsFromRollups(rows, since, until)
	if all.TotalChecks != 25 || all.MinLatency != 50 || all.MaxLatency != 400 {
		t.Fatalf("overall stats = %+v, want 25 checks, min 50, max 400", all)
	}
}

// An hour with no responsive check carries no latency (refresh_rollups writes
// zeros there), and must not drag min/avg down to 0 — it only adds to the check
// count.
func TestComputeStatsFromRollupsSkipsLatencylessBuckets(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	since, until := base, base.Add(48*time.Hour)

	rows := []rollupRow{
		{Bucket: base.Add(1 * time.Hour), Checks: 10, LatencyChecks: 10, SumLatency: 1000, MinLatency: 80, MaxLatency: 120},
		// A full hour of DOWN checks: counted, but latency-less.
		{Bucket: base.Add(2 * time.Hour), Checks: 60, LatencyChecks: 0, SumLatency: 0, MinLatency: 0, MaxLatency: 0},
		{Bucket: base.Add(3 * time.Hour), Checks: 10, LatencyChecks: 10, SumLatency: 1400, MinLatency: 100, MaxLatency: 200},
	}

	stats := computeStatsFromRollups(rows, since, until)

	if stats.TotalChecks != 80 {
		t.Errorf("TotalChecks: got %d, want 80 (every check counts)", stats.TotalChecks)
	}
	if stats.MinLatency != 80 || stats.MaxLatency != 200 {
		t.Errorf("latency range: got [%d,%d], want [80,200]", stats.MinLatency, stats.MaxLatency)
	}
	if want := 2400.0 / 20.0; stats.AvgLatency != want {
		t.Errorf("AvgLatency: got %v, want %v (20 responsive samples, not 80)", stats.AvgLatency, want)
	}
	if len(stats.Points) != 2 {
		t.Errorf("got %d points, want 2 — the DOWN-only hour plots nothing", len(stats.Points))
	}
}

// The degenerate case: the whole window is down. There is no response time to
// report, so the aggregates stay at zero rather than reporting the timeout.
func TestComputeStatsFromRollupsAllDown(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rows := []rollupRow{
		{Bucket: base.Add(1 * time.Hour), Checks: 60, LatencyChecks: 0},
		{Bucket: base.Add(2 * time.Hour), Checks: 60, LatencyChecks: 0},
	}

	stats := computeStatsFromRollups(rows, base, base.Add(48*time.Hour))

	if stats.TotalChecks != 120 {
		t.Errorf("TotalChecks: got %d, want 120", stats.TotalChecks)
	}
	if stats.MinLatency != 0 || stats.MaxLatency != 0 || stats.AvgLatency != 0 {
		t.Errorf("all-down window should report no latency, got %+v", stats)
	}
	if len(stats.Points) != 0 {
		t.Errorf("got %d points, want 0", len(stats.Points))
	}
}
