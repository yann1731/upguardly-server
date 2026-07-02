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
