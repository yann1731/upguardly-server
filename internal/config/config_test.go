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
		{"single", "na-east", []string{"na-east"}},
		{"multiple with spaces", " na-east , eu-west ", []string{"na-east", "eu-west"}},
		{"dedupes", "na-east,na-east,eu-west", []string{"na-east", "eu-west"}},
		{"drops unknown", "na-east,mars-north", []string{"na-east"}},
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
