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
