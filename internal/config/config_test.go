package config

import (
	"reflect"
	"testing"

	"upguardly-backend/internal/models"
)

func TestParseAvailableRegions(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"single", "ca-east", []string{"ca-east"}},
		{"multiple with spaces", " ca-east , us-west ", []string{"ca-east", "us-west"}},
		{"dedupes", "ca-east,ca-east,us-west", []string{"ca-east", "us-west"}},
		{"drops unknown", "ca-east,mars-north", []string{"ca-east"}},
		{"all unknown falls back to default", "mars-north,venus-south", []string{models.DefaultRegion}},
		{"empty falls back to default", "", []string{models.DefaultRegion}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseAvailableRegions(tt.raw); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseAvailableRegions(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}
