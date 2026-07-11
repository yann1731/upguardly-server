-- One-time region id rename: na-east → ca-east. The registry rule is that ids
-- are never renamed, but na-east was a misnomer (the primary pool runs in OVH
-- BHS — Beauharnois, Canada) and was renamed while the feature was ~1 week old,
-- before any satellite region existed. The registry (internal/models/region.go)
-- and the struct-tag defaults (internal/database/bun/models.go) change in the
-- same release; the scheduler binary refuses to run as a region outside its
-- registry, so this migration and the image upgrade are atomic by construction.
--
-- Ordering guard (docs/runbooks/multi-region.md): the primary scheduler must be
-- STOPPED before this runs — a live na-east pool writing between the UPDATEs
-- and its own restart would recreate na-east rows that nothing reads.

-- Transient coordination state: delete rather than rename. Heartbeats are
-- re-upserted within seconds of the new pool starting; verification requests
-- are one-shot rows that expire in 60s anyway.
DELETE FROM scheduler_region_heartbeats;
DELETE FROM region_verification_requests;

-- Live-state and data tables. The PKs of monitor_region_status and
-- monitor_result_rollups include region, so a defensive delete of any
-- pre-existing ca-east rows (there should be none) keeps the UPDATEs
-- conflict-free.
DELETE FROM monitor_region_status WHERE region = 'ca-east';
UPDATE monitor_region_status SET region = 'ca-east' WHERE region = 'na-east';

UPDATE monitors
   SET regions = array_replace(regions, 'na-east', 'ca-east')
 WHERE 'na-east' = ANY(regions);
ALTER TABLE monitors ALTER COLUMN regions SET DEFAULT ARRAY['ca-east']::TEXT[];

DELETE FROM monitor_result_rollups WHERE region = 'ca-east';
UPDATE monitor_result_rollups SET region = 'ca-east' WHERE region = 'na-east';

-- Historical results are renamed too (not left behind, unlike the old
-- "Non-na-east primaries" allowance): na-east leaves the registry entirely, so
-- leftover rows would 400 the ?region= filter and render raw-id chips in the
-- UI for the whole retention window. Seq scan + rewrite of the partitioned
-- table — acceptable at current volume (feature is ~1 week old); if this ever
-- re-runs against a large table, batch it outside the transaction instead.
UPDATE monitor_results SET region = 'ca-east' WHERE region = 'na-east';
ALTER TABLE monitor_results ALTER COLUMN region SET DEFAULT 'ca-east';
