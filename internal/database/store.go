package database

import (
	"context"
	"log"
	"time"

	db "upguardly-backend/internal/database/prisma"
	"upguardly-backend/internal/models"
)

// PrismaStore implements models.Store using the Prisma client.
type PrismaStore struct {
	client *Client
}

func NewPrismaStore(client *Client) *PrismaStore {
	return &PrismaStore{client: client}
}

// ── Monitor ──────────────────────────────────────────────────────────────────

func (s *PrismaStore) CreateMonitor(ctx context.Context, userId, orgId, name, monitorType, target string, interval, timeout int, enabled bool, regions []string) (*models.Monitor, error) {
	optional := []db.MonitorSetParam{
		db.Monitor.Interval.Set(interval),
		db.Monitor.Timeout.Set(timeout),
		db.Monitor.Enabled.Set(enabled),
		db.Monitor.Regions.Set(regions),
	}
	// Solo (FREE/PRO) monitors have no org; only link one when given.
	if orgId != "" {
		optional = append(optional, db.Monitor.Org.Link(db.Organization.ID.Equals(orgId)))
	}

	m, err := s.client.Prisma.Monitor.CreateOne(
		db.Monitor.UserID.Set(userId),
		db.Monitor.Name.Set(name),
		db.Monitor.Type.Set(db.MonitorType(monitorType)),
		db.Monitor.Target.Set(target),
		optional...,
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return monitorToModel(m), nil
}

func (s *PrismaStore) CountMonitorsByOrg(ctx context.Context, orgId string) (int, error) {
	ms, err := s.client.Prisma.Monitor.FindMany(
		db.Monitor.OrgID.Equals(orgId),
	).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return len(ms), nil
}

// CountMonitorsByUser counts a user's solo (org-less) monitors, used to enforce
// FREE/PRO plan limits for accounts not operating inside an organization.
func (s *PrismaStore) CountMonitorsByUser(ctx context.Context, userId string) (int, error) {
	ms, err := s.client.Prisma.Monitor.FindMany(
		db.Monitor.UserID.Equals(userId),
		db.Monitor.OrgID.IsNull(),
	).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return len(ms), nil
}

func (s *PrismaStore) ListMonitors(ctx context.Context, userId string) ([]models.Monitor, error) {
	ms, err := s.client.Prisma.Monitor.FindMany(
		db.Monitor.Or(
			db.Monitor.UserID.Equals(userId),
			db.Monitor.Org.Where(
				db.Organization.Members.Some(
					db.OrganizationMember.UserID.Equals(userId),
				),
			),
		),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.Monitor, len(ms))
	for i := range ms {
		out[i] = *monitorToModel(&ms[i])
	}
	return out, nil
}

func (s *PrismaStore) GetMonitor(ctx context.Context, id, userId string) (*models.Monitor, error) {
	m, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(id),
		db.Monitor.Or(
			db.Monitor.UserID.Equals(userId),
			db.Monitor.Org.Where(
				db.Organization.Members.Some(
					db.OrganizationMember.UserID.Equals(userId),
				),
			),
		),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	return monitorToModel(m), nil
}

func (s *PrismaStore) UpdateMonitor(ctx context.Context, id, userId string, req models.UpdateMonitorRequest) (*models.Monitor, error) {
	if _, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(id),
		db.Monitor.Or(
			db.Monitor.UserID.Equals(userId),
			db.Monitor.Org.Where(
				db.Organization.Members.Some(
					db.OrganizationMember.UserID.Equals(userId),
				),
			),
		),
	).Exec(ctx); err != nil {
		return nil, models.ErrNotFound
	}

	var params []db.MonitorSetParam
	if req.Name != nil {
		params = append(params, db.Monitor.Name.Set(*req.Name))
	}
	if req.Type != nil {
		params = append(params, db.Monitor.Type.Set(db.MonitorType(*req.Type)))
	}
	if req.Target != nil {
		params = append(params, db.Monitor.Target.Set(*req.Target))
	}
	if req.Interval != nil {
		params = append(params, db.Monitor.Interval.Set(*req.Interval))
	}
	if req.Timeout != nil {
		params = append(params, db.Monitor.Timeout.Set(*req.Timeout))
	}
	if req.Enabled != nil {
		params = append(params, db.Monitor.Enabled.Set(*req.Enabled))
	}
	if req.Regions != nil {
		params = append(params, db.Monitor.Regions.Set(*req.Regions))
	}

	m, err := s.client.Prisma.Monitor.FindUnique(
		db.Monitor.ID.Equals(id),
	).Update(params...).Exec(ctx)
	if err != nil {
		return nil, err
	}

	// Best-effort cleanup of status rows for regions no longer configured.
	// Quorum and the region-status endpoint both filter to the configured
	// list, so a failure here is cosmetic (an orphaned row), not correctness.
	if req.Regions != nil {
		if _, err := s.client.Prisma.Prisma.ExecuteRaw(
			`DELETE FROM monitor_region_status
			  WHERE monitor_id = $1 AND NOT (region = ANY(
			        SELECT unnest(regions) FROM monitors WHERE id = $1))`,
			id,
		).Exec(ctx); err != nil {
			log.Printf("Failed to clean up region status rows for %s: %v", id, err)
		}
	}
	return monitorToModel(m), nil
}

func (s *PrismaStore) DeleteMonitor(ctx context.Context, id, userId string) error {
	if _, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(id),
		db.Monitor.Or(
			db.Monitor.UserID.Equals(userId),
			db.Monitor.Org.Where(
				db.Organization.Members.Some(
					db.OrganizationMember.UserID.Equals(userId),
				),
			),
		),
	).Exec(ctx); err != nil {
		return models.ErrNotFound
	}
	_, err := s.client.Prisma.Monitor.FindUnique(
		db.Monitor.ID.Equals(id),
	).Delete().Exec(ctx)
	return err
}

// planScopeSQL selects the monitors governed by $1's plan: their solo
// monitors plus every monitor of an organization they own (org monitors
// follow the org owner's plan, regardless of which member created them).
const planScopeSQL = `((user_id = $1 AND org_id IS NULL)
	OR org_id IN (SELECT id FROM organizations WHERE owner_id = $1))`

// ReconcileMonitorsToPlan snaps the monitors governed by a user's plan to the
// limits of the plan they just moved to. Callers invoke it only after the
// effective plan actually changed — a scheduled cancellation keeps its paid
// plan until Stripe ends the period, so any grace has already lapsed here.
//
//   - Downgrade: intervals faster than the new plan's minimum are raised to
//     it, and region lists longer than the new cap are trimmed to their first
//     N entries.
//   - Upgrade: intervals sitting exactly at the old plan's minimum (the
//     default every UI-created monitor gets) are lowered to the new minimum,
//     so the faster checks the plan advertises apply to existing monitors
//     too. Explicitly slower intervals are user choices and are kept, as is
//     any interval the monitor's timeout wouldn't fit under.
//
// updated_at is bumped so running schedulers restart the affected jobs with
// the new config on their next sync tick. Returns the number of monitors
// adjusted.
func (s *PrismaStore) ReconcileMonitorsToPlan(ctx context.Context, userId, oldPlan, newPlan string) (int, error) {
	oldLimits := models.LimitsForPlan(oldPlan)
	newLimits := models.LimitsForPlan(newPlan)
	total := 0

	if newLimits.MinInterval > oldLimits.MinInterval {
		res, err := s.client.Prisma.Prisma.ExecuteRaw(
			`UPDATE monitors SET interval = $2, updated_at = now()
			  WHERE `+planScopeSQL+` AND interval < $2`,
			userId, newLimits.MinInterval,
		).Exec(ctx)
		if err != nil {
			return total, err
		}
		total += res.Count
	} else if newLimits.MinInterval < oldLimits.MinInterval {
		res, err := s.client.Prisma.Prisma.ExecuteRaw(
			`UPDATE monitors SET interval = $2, updated_at = now()
			  WHERE `+planScopeSQL+` AND interval = $3 AND timeout < $2`,
			userId, newLimits.MinInterval, oldLimits.MinInterval,
		).Exec(ctx)
		if err != nil {
			return total, err
		}
		total += res.Count
	}

	if newLimits.MaxRegions != models.Unlimited {
		res, err := s.client.Prisma.Prisma.ExecuteRaw(
			`UPDATE monitors SET regions = regions[1:($2::int)], updated_at = now()
			  WHERE `+planScopeSQL+` AND cardinality(regions) > $2`,
			userId, newLimits.MaxRegions,
		).Exec(ctx)
		if err != nil {
			return total, err
		}
		total += res.Count

		// Best-effort cleanup of status rows for regions just trimmed off, as
		// UpdateMonitor does: quorum and the region-status endpoint filter to
		// the configured list, so a failure here is cosmetic.
		if res.Count > 0 {
			if _, err := s.client.Prisma.Prisma.ExecuteRaw(
				`DELETE FROM monitor_region_status mrs
				  USING monitors m
				  WHERE mrs.monitor_id = m.id
				    AND ((m.user_id = $1 AND m.org_id IS NULL)
				         OR m.org_id IN (SELECT id FROM organizations WHERE owner_id = $1))
				    AND NOT (mrs.region = ANY(m.regions))`,
				userId,
			).Exec(ctx); err != nil {
				log.Printf("Failed to clean up region status rows for user %s: %v", userId, err)
			}
		}
	}

	return total, nil
}

func (s *PrismaStore) GetMonitorResults(ctx context.Context, monitorId, userId string, limit int, region string) ([]models.MonitorResult, error) {
	if _, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(monitorId),
		db.Monitor.Or(
			db.Monitor.UserID.Equals(userId),
			db.Monitor.Org.Where(
				db.Organization.Members.Some(
					db.OrganizationMember.UserID.Equals(userId),
				),
			),
		),
	).Exec(ctx); err != nil {
		return nil, models.ErrNotFound
	}

	filters := []db.MonitorResultWhereParam{
		db.MonitorResult.MonitorID.Equals(monitorId),
	}
	if region != "" {
		filters = append(filters, db.MonitorResult.Region.Equals(region))
	}
	rs, err := s.client.Prisma.MonitorResult.FindMany(filters...).OrderBy(
		db.MonitorResult.CheckedAt.Order(db.DESC),
	).Take(limit).Exec(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]models.MonitorResult, len(rs))
	for i := range rs {
		out[i] = *resultToModel(&rs[i])
	}
	return out, nil
}

// ListMonitorRegionStatus returns the latest per-region outcome for the
// monitor's configured regions, with staleness computed against the same
// 3x-interval window quorum uses (models.RegionStaleMultiplier).
func (s *PrismaStore) ListMonitorRegionStatus(ctx context.Context, monitorId, userId string) ([]models.MonitorRegionStatus, error) {
	m, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(monitorId),
		db.Monitor.Or(
			db.Monitor.UserID.Equals(userId),
			db.Monitor.Org.Where(
				db.Organization.Members.Some(
					db.OrganizationMember.UserID.Equals(userId),
				),
			),
		),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}

	rows, err := s.client.Prisma.MonitorRegionStatus.FindMany(
		db.MonitorRegionStatus.MonitorID.Equals(monitorId),
	).OrderBy(
		db.MonitorRegionStatus.Region.Order(db.ASC),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}

	configured := make(map[string]bool, len(m.Regions))
	for _, r := range m.Regions {
		configured[r] = true
	}
	staleAfter := time.Duration(m.Interval*models.RegionStaleMultiplier) * time.Second

	out := make([]models.MonitorRegionStatus, 0, len(rows))
	for i := range rows {
		if !configured[rows[i].Region] {
			continue
		}
		st := models.MonitorRegionStatus{
			Region:    rows[i].Region,
			Status:    models.Status(rows[i].Status),
			Latency:   rows[i].Latency,
			CheckedAt: rows[i].CheckedAt,
			Stale:     time.Since(rows[i].CheckedAt) > staleAfter,
		}
		if code, ok := rows[i].StatusCode(); ok {
			st.StatusCode = &code
		}
		if msg, ok := rows[i].Message(); ok {
			st.Message = &msg
		}
		out = append(out, st)
	}
	return out, nil
}

// ── Incidents & stats ──────────────────────────────────────────────────────────

func (s *PrismaStore) ListIncidents(ctx context.Context, monitorId, userId string, limit int) ([]models.Incident, error) {
	if _, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(monitorId),
		db.Monitor.Or(
			db.Monitor.UserID.Equals(userId),
			db.Monitor.Org.Where(
				db.Organization.Members.Some(
					db.OrganizationMember.UserID.Equals(userId),
				),
			),
		),
	).Exec(ctx); err != nil {
		return nil, models.ErrNotFound
	}

	is, err := s.client.Prisma.Incident.FindMany(
		db.Incident.MonitorID.Equals(monitorId),
	).OrderBy(
		db.Incident.StartedAt.Order(db.DESC),
	).Take(limit).Exec(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]models.Incident, len(is))
	for i := range is {
		out[i] = *incidentToModel(&is[i])
	}
	return out, nil
}

func (s *PrismaStore) GetMonitorStats(ctx context.Context, monitorId, userId string, since time.Time) (*models.MonitorStats, error) {
	if _, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(monitorId),
		db.Monitor.Or(
			db.Monitor.UserID.Equals(userId),
			db.Monitor.Org.Where(
				db.Organization.Members.Some(
					db.OrganizationMember.UserID.Equals(userId),
				),
			),
		),
	).Exec(ctx); err != nil {
		return nil, models.ErrNotFound
	}

	until := time.Now()

	var stats *models.MonitorStats
	if until.Sub(since) <= rawStatsWindow {
		// Short window (≤24h view): read raw results for an exact series.
		rs, err := s.client.Prisma.MonitorResult.FindMany(
			db.MonitorResult.MonitorID.Equals(monitorId),
			db.MonitorResult.CheckedAt.Gte(since),
		).OrderBy(
			db.MonitorResult.CheckedAt.Order(db.ASC),
		).Exec(ctx)
		if err != nil {
			return nil, err
		}
		stats = computeStats(rs, since, until)

		// Per-region series from the same rows (order within a region is
		// preserved by the filter, so each group stays CheckedAt-ascending).
		for _, region := range resultRegions(rs) {
			var group []db.MonitorResultModel
			for i := range rs {
				if rs[i].Region == region {
					group = append(group, rs[i])
				}
			}
			rstats := computeStats(group, since, until)
			stats.Regions = append(stats.Regions, models.RegionStats{
				Region:      region,
				MinLatency:  rstats.MinLatency,
				MaxLatency:  rstats.MaxLatency,
				AvgLatency:  rstats.AvgLatency,
				TotalChecks: rstats.TotalChecks,
				Points:      rstats.Points,
			})
		}
	} else {
		// Long window (7d/30d): read hourly rollups instead of every raw row.
		rus, err := s.client.Prisma.MonitorResultRollup.FindMany(
			db.MonitorResultRollup.MonitorID.Equals(monitorId),
			db.MonitorResultRollup.Bucket.Gte(since.Truncate(time.Hour)),
		).OrderBy(
			db.MonitorResultRollup.Bucket.Order(db.ASC),
		).Exec(ctx)
		if err != nil {
			return nil, err
		}
		rows := make([]rollupRow, len(rus))
		for i := range rus {
			rows[i] = rollupRow{
				Region:     rus[i].Region,
				Bucket:     rus[i].Bucket,
				Checks:     rus[i].Checks,
				SumLatency: rus[i].SumLatency,
				MinLatency: rus[i].MinLatency,
				MaxLatency: rus[i].MaxLatency,
			}
		}
		stats = computeStatsFromRollups(rows, since, until)

		for _, region := range rollupRegions(rows) {
			var group []rollupRow
			for i := range rows {
				if rows[i].Region == region {
					group = append(group, rows[i])
				}
			}
			rstats := computeStatsFromRollups(group, since, until)
			stats.Regions = append(stats.Regions, models.RegionStats{
				Region:      region,
				MinLatency:  rstats.MinLatency,
				MaxLatency:  rstats.MaxLatency,
				AvgLatency:  rstats.AvgLatency,
				TotalChecks: rstats.TotalChecks,
				Points:      rstats.Points,
			})
		}
	}

	incidents, err := s.client.Prisma.Incident.FindMany(
		db.Incident.MonitorID.Equals(monitorId),
		db.Incident.StartedAt.Gte(since),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	stats.IncidentCount = len(incidents)

	return stats, nil
}

// statBuckets is the number of time buckets used for the response-time graph.
const statBuckets = 48

// computeStats derives min/max/avg latency and a bucketed average-latency series
// from monitor results ordered ascending by CheckedAt. The window [since, until]
// is split into statBuckets equal buckets; empty buckets are omitted.
func computeStats(rs []db.MonitorResultModel, since, until time.Time) *models.MonitorStats {
	stats := &models.MonitorStats{Points: []models.StatPoint{}}
	if len(rs) == 0 {
		return stats
	}

	min, max, sum := rs[0].Latency, rs[0].Latency, 0
	for i := range rs {
		l := rs[i].Latency
		if l < min {
			min = l
		}
		if l > max {
			max = l
		}
		sum += l
	}
	stats.MinLatency = min
	stats.MaxLatency = max
	stats.TotalChecks = len(rs)
	stats.AvgLatency = float64(sum) / float64(len(rs))

	span := until.Sub(since)
	if span <= 0 {
		span = time.Second
	}
	bucketDur := span / statBuckets

	type acc struct {
		sum   int
		count int
	}
	buckets := make([]acc, statBuckets)
	for i := range rs {
		idx := int(rs[i].CheckedAt.Sub(since) / bucketDur)
		if idx < 0 {
			idx = 0
		}
		if idx >= statBuckets {
			idx = statBuckets - 1
		}
		buckets[idx].sum += rs[i].Latency
		buckets[idx].count++
	}
	for i, b := range buckets {
		if b.count == 0 {
			continue
		}
		stats.Points = append(stats.Points, models.StatPoint{
			Timestamp:  since.Add(time.Duration(i)*bucketDur + bucketDur/2),
			AvgLatency: float64(b.sum) / float64(b.count),
		})
	}
	return stats
}

// rawStatsWindow is the cutoff below which stats come from raw monitor_results
// (exact). Longer windows read hourly rollups. 25h covers the 24h view with
// margin; the 7d/30d periods fall to the rollup path.
const rawStatsWindow = 25 * time.Hour

// rollupRow is a plain projection of an hourly rollup, decoupled from the Prisma
// model so the aggregation can be unit-tested without a database.
type rollupRow struct {
	Region     string
	Bucket     time.Time
	Checks     int
	SumLatency int
	MinLatency int
	MaxLatency int
}

// resultRegions returns the distinct regions present in rs, in first-seen
// order (stable for the response and tests).
func resultRegions(rs []db.MonitorResultModel) []string {
	var regions []string
	seen := make(map[string]bool)
	for i := range rs {
		if !seen[rs[i].Region] {
			seen[rs[i].Region] = true
			regions = append(regions, rs[i].Region)
		}
	}
	return regions
}

// rollupRegions is resultRegions for rollup rows.
func rollupRegions(rows []rollupRow) []string {
	var regions []string
	seen := make(map[string]bool)
	for i := range rows {
		if !seen[rows[i].Region] {
			seen[rows[i].Region] = true
			regions = append(regions, rows[i].Region)
		}
	}
	return regions
}

// computeStatsFromRollups mirrors computeStats but works from hourly rollups.
// Scalar aggregates (min/max/avg/total) are exact; the bucketed series assigns
// each whole hour to one of statBuckets buckets, so the graph is a close
// approximation at bucket boundaries — acceptable for 7d/30d trend views.
func computeStatsFromRollups(rows []rollupRow, since, until time.Time) *models.MonitorStats {
	stats := &models.MonitorStats{Points: []models.StatPoint{}}
	if len(rows) == 0 {
		return stats
	}

	min, max, totalSum, totalCount := rows[0].MinLatency, rows[0].MaxLatency, 0, 0
	for i := range rows {
		if rows[i].MinLatency < min {
			min = rows[i].MinLatency
		}
		if rows[i].MaxLatency > max {
			max = rows[i].MaxLatency
		}
		totalSum += rows[i].SumLatency
		totalCount += rows[i].Checks
	}
	if totalCount == 0 {
		return stats
	}
	stats.MinLatency = min
	stats.MaxLatency = max
	stats.TotalChecks = totalCount
	stats.AvgLatency = float64(totalSum) / float64(totalCount)

	span := until.Sub(since)
	if span <= 0 {
		span = time.Second
	}
	bucketDur := span / statBuckets

	type acc struct {
		sum   int
		count int
	}
	buckets := make([]acc, statBuckets)
	for i := range rows {
		idx := int(rows[i].Bucket.Sub(since) / bucketDur)
		if idx < 0 {
			idx = 0
		}
		if idx >= statBuckets {
			idx = statBuckets - 1
		}
		buckets[idx].sum += rows[i].SumLatency
		buckets[idx].count += rows[i].Checks
	}
	for i, b := range buckets {
		if b.count == 0 {
			continue
		}
		stats.Points = append(stats.Points, models.StatPoint{
			Timestamp:  since.Add(time.Duration(i)*bucketDur + bucketDur/2),
			AvgLatency: float64(b.sum) / float64(b.count),
		})
	}
	return stats
}

// ── Conversion helpers ────────────────────────────────────────────────────────

func monitorToModel(m *db.MonitorModel) *models.Monitor {
	var orgID *string
	if id, ok := m.OrgID(); ok {
		orgID = &id
	}
	return &models.Monitor{
		ID:        m.ID,
		OrgID:     orgID,
		Name:      m.Name,
		Type:      models.MonitorType(m.Type),
		Target:    m.Target,
		Interval:  m.Interval,
		Timeout:   m.Timeout,
		Enabled:   m.Enabled,
		Regions:   m.Regions,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func incidentToModel(i *db.IncidentModel) *models.Incident {
	inc := &models.Incident{
		ID:        i.ID,
		MonitorID: i.MonitorID,
		Status:    models.Status(i.Status),
		StartedAt: i.StartedAt,
	}
	if resolved, ok := i.ResolvedAt(); ok {
		r := resolved
		inc.ResolvedAt = &r
		d := resolved.Sub(i.StartedAt).Milliseconds()
		inc.DurationMs = &d
	}
	if code, ok := i.StatusCode(); ok {
		inc.StatusCode = &code
	}
	if msg, ok := i.Message(); ok {
		inc.Message = &msg
	}
	return inc
}

func resultToModel(r *db.MonitorResultModel) *models.MonitorResult {
	res := &models.MonitorResult{
		ID:        r.ID,
		MonitorID: r.MonitorID,
		Status:    models.Status(r.Status),
		Latency:   r.Latency,
		Region:    r.Region,
		CheckedAt: r.CheckedAt,
	}
	if code, ok := r.StatusCode(); ok {
		res.StatusCode = &code
	}
	if msg, ok := r.Message(); ok {
		res.Message = &msg
	}
	return res
}
