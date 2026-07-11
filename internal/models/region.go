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
// (Broken exactly once, deliberately: na-east → ca-east, with a full data
// migration — 20260709120000_rename_na_east_to_ca_east. eu-west and
// ap-southeast were dropped at the same time; they were never in
// AVAILABLE_REGIONS and had no data. Any future rename needs the same
// treatment: every region-bearing table AND its column defaults.)
var Regions = []Region{
	{ID: "ca-east", Name: "Canada (East)"},
	{ID: "us-west", Name: "US West"},
	{ID: "eu-west-fr", Name: "Europe (France)"},
	{ID: "eu-west-de", Name: "Europe (Germany)"},
}

// DefaultRegion is the region assumed for everything that predates
// multi-region: existing monitors and results are backfilled to it, and a
// scheduler without SCHEDULER_REGION runs as it.
const DefaultRegion = "ca-east"

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

// CheckSource distinguishes a monitor's normal per-interval check from a
// one-off cross-region confirmation triggered by another region's failure. It
// controls whether recording the check enqueues further confirmations (only
// SourceScheduled does — verification checks never fan out, to avoid a loop).
type CheckSource string

const (
	SourceScheduled    CheckSource = "SCHEDULED"
	SourceVerification CheckSource = "VERIFICATION"
)

// VerificationRequest is a claimed one-off confirmation check: a region has
// been asked to verify a monitor another region just reported unhealthy. It
// carries the monitor fields the verifier needs to run the check without a
// second query.
type VerificationRequest struct {
	ID        string
	MonitorID string
	Region    string
	Type      MonitorType
	Target    string
	Timeout   int
}
