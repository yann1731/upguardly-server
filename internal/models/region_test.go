package models

import "testing"

func TestRegionRegistry(t *testing.T) {
	if len(Regions) == 0 {
		t.Fatal("region registry is empty")
	}

	seen := make(map[string]bool)
	for _, r := range Regions {
		if r.ID == "" || r.Name == "" {
			t.Errorf("region %+v has empty id or name", r)
		}
		if seen[r.ID] {
			t.Errorf("duplicate region id %q", r.ID)
		}
		seen[r.ID] = true
	}

	if !seen[DefaultRegion] {
		t.Errorf("DefaultRegion %q is not in the registry", DefaultRegion)
	}
}

func TestValidRegion(t *testing.T) {
	if !ValidRegion(DefaultRegion) {
		t.Errorf("ValidRegion(%q) = false, want true", DefaultRegion)
	}
	// na-east is deliberately in the bad list: it left the registry in the
	// 2026-07 rename and must never validate again.
	for _, bad := range []string{"", "na-east", "CA-EAST", " ca-east"} {
		if ValidRegion(bad) {
			t.Errorf("ValidRegion(%q) = true, want false", bad)
		}
	}
}

func TestRegionByID(t *testing.T) {
	r, ok := RegionByID("eu-west-fr")
	if !ok || r.Name != "Europe (France)" {
		t.Errorf("RegionByID(eu-west-fr) = %+v, %v", r, ok)
	}
	if _, ok := RegionByID("nope"); ok {
		t.Error("RegionByID(nope) unexpectedly found")
	}
}

func TestNormalizeRegions(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr bool
	}{
		{"single", []string{"ca-east"}, []string{"ca-east"}, false},
		{"dedupes preserving order", []string{"us-west", "ca-east", "us-west"}, []string{"us-west", "ca-east"}, false},
		{"unknown region", []string{"ca-east", "mars-north"}, nil, true},
		{"empty list", []string{}, nil, true},
		{"empty string entry", []string{""}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeRegions(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeRegions(%v) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("NormalizeRegions(%v) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("NormalizeRegions(%v) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}

func TestRegionIDs(t *testing.T) {
	ids := RegionIDs()
	if len(ids) != len(Regions) {
		t.Fatalf("RegionIDs len = %d, want %d", len(ids), len(Regions))
	}
	for i, r := range Regions {
		if ids[i] != r.ID {
			t.Errorf("RegionIDs[%d] = %q, want %q", i, ids[i], r.ID)
		}
	}
}
