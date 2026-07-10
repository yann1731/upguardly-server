package models

import "testing"

func TestChannelAllowed(t *testing.T) {
	tests := []struct {
		plan    string
		channel AlertChannel
		want    bool
	}{
		{"FREE", AlertChannelEMAIL, true},
		{"FREE", AlertChannelSMS, true},
		{"FREE", AlertChannelDISCORD, true},
		{"FREE", AlertChannelSLACK, false},
		{"FREE", AlertChannelTELEGRAM, false},
		{"PRO", AlertChannelSLACK, true},
		{"PRO", AlertChannelTELEGRAM, true},
		{"ENTERPRISE", AlertChannelSLACK, true},
		{"ENTERPRISE", AlertChannelTELEGRAM, true},
		// Unknown plans fall back to FREE.
		{"", AlertChannelTELEGRAM, false},
		{"BOGUS", AlertChannelEMAIL, true},
	}

	for _, tt := range tests {
		if got := LimitsForPlan(tt.plan).ChannelAllowed(tt.channel); got != tt.want {
			t.Errorf("LimitsForPlan(%q).ChannelAllowed(%s) = %v, want %v", tt.plan, tt.channel, got, tt.want)
		}
	}
}

func TestMaxRegionsPerPlan(t *testing.T) {
	tests := []struct {
		plan string
		want int
	}{
		{"FREE", 1},
		{"PRO", 3},
		{"ENTERPRISE", Unlimited},
		// Unknown plans fall back to FREE.
		{"", 1},
		{"BOGUS", 1},
	}

	for _, tt := range tests {
		if got := LimitsForPlan(tt.plan).MaxRegions; got != tt.want {
			t.Errorf("LimitsForPlan(%q).MaxRegions = %d, want %d", tt.plan, got, tt.want)
		}
	}
}

func TestEffectiveInterval(t *testing.T) {
	ptr := func(i int) *int { return &i }
	tests := []struct {
		name    string
		raw     *int
		plan    string
		timeout int
		want    int
	}{
		{"explicit override returned as-is", ptr(120), "FREE", 30, 120},
		{"explicit below plan floor still returned (validated at write)", ptr(60), "FREE", 30, 60},
		{"follow-plan FREE resolves to 300", nil, "FREE", 30, 300},
		{"follow-plan PRO resolves to 60", nil, "PRO", 30, 60},
		{"follow-plan unknown plan falls back to FREE floor", nil, "BOGUS", 30, 300},
		{"follow-plan clamps up so timeout stays below interval", nil, "PRO", 90, 91},
		{"timeout equal to floor also clamps", nil, "PRO", 60, 61},
	}
	for _, tt := range tests {
		if got := EffectiveInterval(tt.raw, tt.plan, tt.timeout); got != tt.want {
			t.Errorf("%s: EffectiveInterval(%v, %q, %d) = %d, want %d", tt.name, tt.raw, tt.plan, tt.timeout, got, tt.want)
		}
	}
}
