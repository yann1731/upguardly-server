package bun

import (
	"math"
	"testing"
	"time"
)

// day builds a bucketed row for the Nth day before `today`.
func day(today time.Time, daysAgo int, region string, available, total int) dailyUptimeRow {
	return dailyUptimeRow{
		Day:       today.AddDate(0, 0, -daysAgo),
		Region:    region,
		Available: available,
		Total:     total,
	}
}

// The strip must always be exactly as long as requested and run oldest-first,
// so the UI can render a fixed number of cells without padding.
func TestComputeDailyUptimeAlwaysFillsWindow(t *testing.T) {
	today := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)

	days, _ := computeDailyUptime(nil, today, 31)
	if len(days) != 31 {
		t.Fatalf("len(days) = %d, want 31", len(days))
	}
	if days[0].Date != "2026-08-01" {
		t.Errorf("days[0].Date = %q, want 2026-08-01 (oldest first)", days[0].Date)
	}
	if days[30].Date != "2026-08-31" {
		t.Errorf("days[30].Date = %q, want 2026-08-31 (today last)", days[30].Date)
	}
	for _, d := range days {
		if d.Monitored || d.UptimePercent != nil {
			t.Fatalf("day %s: no rows should mean not-monitored, got %+v", d.Date, d)
		}
	}
}

// A day with no rows is a gap in monitoring, not a 0% outage — the two look
// very different on the strip.
func TestComputeDailyUptimeGapsAreNotOutages(t *testing.T) {
	today := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	rows := []dailyUptimeRow{
		day(today, 2, "ca-east", 100, 100),
		// one day ago: nothing recorded
		day(today, 0, "ca-east", 0, 100),
	}

	days, _ := computeDailyUptime(rows, today, 3)

	if !days[0].Monitored || *days[0].UptimePercent != 100 {
		t.Errorf("oldest day = %+v, want monitored at 100%%", days[0])
	}
	if days[1].Monitored || days[1].UptimePercent != nil {
		t.Errorf("gap day = %+v, want not monitored", days[1])
	}
	if !days[2].Monitored || *days[2].UptimePercent != 0 {
		t.Errorf("today = %+v, want monitored at 0%% (a real outage)", days[2])
	}
}

// Each region contributes equally regardless of how many checks it ran, so a
// fast-checking region can't drown out a slower one.
func TestComputeDailyUptimeAveragesAcrossRegions(t *testing.T) {
	today := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	rows := []dailyUptimeRow{
		// A region checking 10x as often, fully up...
		day(today, 0, "ca-east", 1000, 1000),
		// ...must not outvote a slow region that was entirely down.
		day(today, 0, "eu-west-fr", 0, 100),
	}

	days, _ := computeDailyUptime(rows, today, 1)

	if got := *days[0].UptimePercent; math.Abs(got-50) > 1e-9 {
		t.Errorf("uptimePercent = %v, want 50 (unweighted mean of 100 and 0)", got)
	}
}

// uptime7d is check-weighted within a region: a day with few checks must not
// count as much as a busy one, which averaging the daily percentages would do.
func TestComputeDailyUptimeSevenDayIsCheckWeighted(t *testing.T) {
	today := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	rows := []dailyUptimeRow{
		day(today, 0, "ca-east", 990, 1000), // busy day, 99%
		day(today, 1, "ca-east", 0, 10),     // quiet day, 0%
	}

	_, uptime7d := computeDailyUptime(rows, today, 31)

	// Check-weighted: 990/1010. Averaging the daily percentages would give 49.5.
	want := 990.0 / 1010.0 * 100
	if math.Abs(uptime7d-want) > 1e-9 {
		t.Errorf("uptime7d = %v, want %v (check-weighted, not a mean of daily percentages)", uptime7d, want)
	}
}

// Only the trailing 7 days feed uptime7d, even though the strip is longer.
func TestComputeDailyUptimeSevenDayWindowExcludesOlderDays(t *testing.T) {
	today := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	rows := []dailyUptimeRow{
		day(today, 6, "ca-east", 100, 100), // inside the window
		day(today, 7, "ca-east", 0, 100),   // just outside it
	}

	_, uptime7d := computeDailyUptime(rows, today, 31)

	if math.Abs(uptime7d-100) > 1e-9 {
		t.Errorf("uptime7d = %v, want 100 — the 8-day-old outage is outside the window", uptime7d)
	}
}

// The window ends at the caller's today, so a non-UTC offset shifts which
// calendar dates the strip covers.
func TestComputeDailyUptimeHonoursCallerCalendar(t *testing.T) {
	// A caller in UTC+13 whose local date has already rolled over to Sep 1.
	today := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rows := []dailyUptimeRow{day(today, 0, "ca-east", 50, 100)}

	days, _ := computeDailyUptime(rows, today, 2)

	if days[1].Date != "2026-09-01" {
		t.Fatalf("last day = %q, want 2026-09-01", days[1].Date)
	}
	if got := *days[1].UptimePercent; got != 50 {
		t.Errorf("uptimePercent = %v, want 50", got)
	}
}
