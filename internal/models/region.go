package models

import "time"

// Region is a geographic location monitors can be checked from. The registry
// below is the set of regions the codebase knows how to display; which of
// them are actually deployed (and therefore selectable on a monitor) is
// deployment config — AVAILABLE_REGIONS, see config.Load. Keeping the two
// separate means adding a region to the registry is a code change, while
// turning it on for users is an ops change after its scheduler pool is up.
type Region struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Regions is the static registry of known regions. IDs are stored on
// monitors and results, so entries must never be renamed — only added.
var Regions = []Region{
	{ID: "na-east", Name: "North America (East)"},
	{ID: "eu-west", Name: "EU West"},
	{ID: "ap-southeast", Name: "Asia Pacific"},
}

// DefaultRegion is the region assumed for everything that predates
// multi-region: existing monitors and results are backfilled to it, and a
// scheduler without SCHEDULER_REGION runs as it.
const DefaultRegion = "na-east"

func ValidRegion(id string) bool {
	_, ok := RegionByID(id)
	return ok
}

func RegionByID(id string) (Region, bool) {
	for _, r := range Regions {
		if r.ID == id {
			return r, true
		}
	}
	return Region{}, false
}

// RegionIDs returns the ids of every registered region, in registry order.
func RegionIDs() []string {
	ids := make([]string, len(Regions))
	for i, r := range Regions {
		ids[i] = r.ID
	}
	return ids
}

// RegionStaleMultiplier is how many missed intervals make a region's last
// report stale. Must match the p_stale_multiplier default of
// maintenance.record_region_check (migration 20260703120000_add_regions):
// quorum and the UI must agree on which regions still count.
const RegionStaleMultiplier = 3

// MonitorRegionStatus is the latest check outcome one region reported for a
// monitor. Stale means the report is older than RegionStaleMultiplier
// intervals — the region's pool has stopped reporting and no longer counts
// toward quorum.
type MonitorRegionStatus struct {
	Region     string    `json:"region"`
	Status     Status    `json:"status"`
	Latency    int       `json:"latency"`
	StatusCode *int      `json:"statusCode,omitempty"`
	Message    *string   `json:"message,omitempty"`
	CheckedAt  time.Time `json:"checkedAt"`
	Stale      bool      `json:"stale"`
}
