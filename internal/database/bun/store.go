package bun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"upguardly-backend/internal/models"
)

var _ models.Store = (*BunStore)(nil)

// mapError translates common database/driver errors to generic domain errors.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return models.ErrNotFound
	}
	if pgErr, ok := err.(pgdriver.Error); ok && pgErr.IntegrityViolation() {
		// Unique constraint or check constraint failure.
		// Postgres code "23505" represents unique_violation.
		if pgErr.Field('C') == "23505" {
			return models.ErrConflict
		}
	}
	return err
}

// ── Monitors ─────────────────────────────────────────────────────────────────

// ownerPlanExpr is a SQL expression yielding a monitor's owner's effective plan
// (org owner's plan for org monitors, else the user's), mirroring
// handlers.effectivePlan: CANCELED subscriptions carry no entitlement, a
// missing subscription is FREE. alias is the monitors table alias in the
// surrounding query ("m" for bun Model selects, "monitors" for raw ones). It is
// selected AS owner_plan into Monitor.OwnerPlan so toModel can resolve
// follow-plan (NULL) intervals.
func ownerPlanExpr(alias string) string {
	return fmt.Sprintf(`COALESCE((
		SELECT CASE WHEN s.status = 'CANCELED' THEN 'FREE' ELSE s.plan::text END
		  FROM subscriptions s
		 WHERE s.user_id = COALESCE(
		           (SELECT o.owner_id FROM organizations o WHERE o.id = %[1]s.org_id),
		           %[1]s.user_id)), 'FREE')`, alias)
}

func (s *BunStore) CreateMonitor(ctx context.Context, userId, orgId, name, monitorType, target string, interval *int, timeout int, degradedThresholdMs, repeatIntervalSecs, repeatMaxCount *int, enabled bool, regions []string) (*models.Monitor, error) {
	var orgIDPtr *string
	if orgId != "" {
		orgIDPtr = &orgId
	}
	m := &Monitor{
		ID:                      uuid.NewString(),
		UserID:                  userId,
		OrgID:                   orgIDPtr,
		Name:                    name,
		Type:                    monitorType,
		Target:                  target,
		Interval:                interval,
		Timeout:                 timeout,
		DegradedThresholdMs:     degradedThresholdMs,
		RepeatAlertIntervalSecs: repeatIntervalSecs,
		RepeatAlertMaxCount:     repeatMaxCount,
		Enabled:                 enabled,
		Regions:                 regions,
		UpdatedAt:               time.Now(),
	}
	if err := s.client.DB.NewInsert().Model(m).ExcludeColumn("created_at").Returning("*").Scan(ctx); err != nil {
		return nil, mapError(err)
	}
	// Returning("*") doesn't include the computed owner_plan; resolve it so the
	// API response shows the correct effective interval for follow-plan monitors.
	if err := s.client.DB.NewRaw(
		"SELECT "+ownerPlanExpr("monitors")+" FROM monitors WHERE id = ?", m.ID,
	).Scan(ctx, &m.OwnerPlan); err != nil {
		return nil, mapError(err)
	}
	model := m.toModel()
	return &model, nil
}

func (s *BunStore) CountMonitorsByOrg(ctx context.Context, orgId string) (int, error) {
	count, err := s.client.DB.NewSelect().
		Model((*Monitor)(nil)).
		Where("org_id = ?", orgId).
		Count(ctx)
	return count, mapError(err)
}

func (s *BunStore) CountMonitorsByUser(ctx context.Context, userId string) (int, error) {
	count, err := s.client.DB.NewSelect().
		Model((*Monitor)(nil)).
		Where("user_id = ?", userId).
		Where("org_id IS NULL").
		Count(ctx)
	return count, mapError(err)
}

func (s *BunStore) ListMonitors(ctx context.Context, userId string) ([]models.Monitor, error) {
	var monitors []Monitor
	err := s.client.DB.NewSelect().
		Model(&monitors).
		ColumnExpr("m.*").
		ColumnExpr(ownerPlanExpr("m")+" AS owner_plan").
		Where("user_id = ? OR org_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)", userId, userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]models.Monitor, len(monitors))
	for i, m := range monitors {
		out[i] = m.toModel()
	}
	return out, nil
}

func (s *BunStore) GetMonitor(ctx context.Context, id, userId string) (*models.Monitor, error) {
	var m Monitor
	err := s.client.DB.NewSelect().
		Model(&m).
		ColumnExpr("m.*").
		ColumnExpr(ownerPlanExpr("m")+" AS owner_plan").
		Where("id = ?", id).
		Where("user_id = ? OR org_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)", userId, userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	model := m.toModel()
	return &model, nil
}

func (s *BunStore) UpdateMonitor(ctx context.Context, id, userId string, req models.UpdateMonitorRequest) (*models.Monitor, error) {
	var m Monitor
	err := s.client.DB.NewSelect().
		Model(&m).
		ColumnExpr("m.*").
		ColumnExpr(ownerPlanExpr("m")+" AS owner_plan").
		Where("id = ?", id).
		Where("user_id = ? OR org_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)", userId, userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	q := s.client.DB.NewUpdate().
		Model(&m).
		Where("id = ?", id)

	var hasUpdates bool
	if req.Name != nil {
		m.Name = *req.Name
		q = q.Set("name = ?", *req.Name)
		hasUpdates = true
	}
	if req.Type != nil {
		m.Type = string(*req.Type)
		q = q.Set("type = ?", string(*req.Type))
		hasUpdates = true
	}
	if req.Target != nil {
		m.Target = *req.Target
		q = q.Set("target = ?", *req.Target)
		hasUpdates = true
	}
	if req.Interval != nil {
		// 0 = revert to follow-plan (store NULL); any other value is explicit.
		if *req.Interval == 0 {
			m.Interval = nil
			q = q.Set("interval = NULL")
		} else {
			v := *req.Interval
			m.Interval = &v
			q = q.Set("interval = ?", v)
		}
		hasUpdates = true
	}
	if req.Timeout != nil {
		m.Timeout = *req.Timeout
		q = q.Set("timeout = ?", *req.Timeout)
		hasUpdates = true
	}
	if req.DegradedThresholdMs != nil {
		// 0 = revert to the per-type default (store NULL); else explicit.
		if *req.DegradedThresholdMs == 0 {
			m.DegradedThresholdMs = nil
			q = q.Set("degraded_threshold_ms = NULL")
		} else {
			v := *req.DegradedThresholdMs
			m.DegradedThresholdMs = &v
			q = q.Set("degraded_threshold_ms = ?", v)
		}
		hasUpdates = true
	}
	if req.RepeatAlertIntervalSecs != nil || req.RepeatAlertMaxCount != nil {
		// The pair is set or cleared together (handler-validated): 0 on
		// either side disables and stores NULL for both.
		iv, cv := 0, 0
		if req.RepeatAlertIntervalSecs != nil {
			iv = *req.RepeatAlertIntervalSecs
		}
		if req.RepeatAlertMaxCount != nil {
			cv = *req.RepeatAlertMaxCount
		}
		if iv == 0 || cv == 0 {
			m.RepeatAlertIntervalSecs = nil
			m.RepeatAlertMaxCount = nil
			q = q.Set("repeat_alert_interval_secs = NULL").Set("repeat_alert_max_count = NULL")
		} else {
			m.RepeatAlertIntervalSecs = &iv
			m.RepeatAlertMaxCount = &cv
			q = q.Set("repeat_alert_interval_secs = ?", iv).Set("repeat_alert_max_count = ?", cv)
		}
		hasUpdates = true
	}
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
		q = q.Set("enabled = ?", *req.Enabled)
		hasUpdates = true
	}
	if req.CertCheckEnabled != nil {
		m.CertCheckEnabled = *req.CertCheckEnabled
		q = q.Set("cert_check_enabled = ?", *req.CertCheckEnabled)
		hasUpdates = true
	}
	if req.CertExpiryThresholdDays != nil {
		m.CertExpiryThresholdDays = *req.CertExpiryThresholdDays
		q = q.Set("cert_expiry_threshold_days = ?", *req.CertExpiryThresholdDays)
		hasUpdates = true
	}
	if req.DomainCheckEnabled != nil {
		m.DomainCheckEnabled = *req.DomainCheckEnabled
		q = q.Set("domain_check_enabled = ?", *req.DomainCheckEnabled)
		hasUpdates = true
	}
	if req.DomainExpiryThresholdDays != nil {
		m.DomainExpiryThresholdDays = *req.DomainExpiryThresholdDays
		q = q.Set("domain_expiry_threshold_days = ?", *req.DomainExpiryThresholdDays)
		hasUpdates = true
	}
	if req.Regions != nil {
		m.Regions = *req.Regions
		q = q.Set("regions = ?", pgdialect.Array(*req.Regions))
		hasUpdates = true
	}

	if hasUpdates {
		m.UpdatedAt = time.Now()
		q = q.Set("updated_at = ?", m.UpdatedAt)
		if _, err := q.Exec(ctx); err != nil {
			return nil, mapError(err)
		}
	}

	if req.Regions != nil {
		if _, err := s.client.DB.NewRaw(
			`DELETE FROM monitor_region_status
			  WHERE monitor_id = ? AND NOT (region = ANY(
			        SELECT unnest(regions) FROM monitors WHERE id = ?))`,
			id, id,
		).Exec(ctx); err != nil {
			log.Printf("Failed to clean up region status rows for %s: %v", id, err)
		}
	}

	model := m.toModel()
	return &model, nil
}

func (s *BunStore) DeleteMonitor(ctx context.Context, id, userId string) error {
	var m Monitor
	err := s.client.DB.NewSelect().
		Model(&m).
		Where("id = ?", id).
		Where("user_id = ? OR org_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)", userId, userId).
		Scan(ctx)
	if err != nil {
		return mapError(err)
	}

	_, err = s.client.DB.NewDelete().
		Model((*Monitor)(nil)).
		Where("id = ?", id).
		Exec(ctx)
	return mapError(err)
}

func (s *BunStore) GetMonitorResults(ctx context.Context, monitorId, userId string, limit int, region string) ([]models.MonitorResult, error) {
	var m Monitor
	err := s.client.DB.NewSelect().
		Model(&m).
		Where("id = ?", monitorId).
		Where("user_id = ? OR org_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)", userId, userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	var results []MonitorResult
	q := s.client.DB.NewSelect().
		Model(&results).
		Where("monitor_id = ?", monitorId)
	if region != "" {
		q = q.Where("region = ?", region)
	}
	err = q.Order("checked_at DESC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	out := make([]models.MonitorResult, len(results))
	for i, r := range results {
		var statusCode *int
		if r.StatusCode != nil {
			c := *r.StatusCode
			statusCode = &c
		}
		var msg *string
		if r.Message != nil {
			mVal := *r.Message
			msg = &mVal
		}
		out[i] = models.MonitorResult{
			ID:         r.ID,
			MonitorID:  r.MonitorID,
			Status:     models.Status(r.Status),
			Latency:    r.Latency,
			StatusCode: statusCode,
			Message:    msg,
			Region:     r.Region,
			CheckedAt:  r.CheckedAt,
		}
	}
	return out, nil
}

func (s *BunStore) ListMonitorRegionStatus(ctx context.Context, monitorId, userId string) ([]models.MonitorRegionStatus, error) {
	var m Monitor
	err := s.client.DB.NewSelect().
		Model(&m).
		ColumnExpr("m.*").
		ColumnExpr(ownerPlanExpr("m")+" AS owner_plan").
		Where("id = ?", monitorId).
		Where("user_id = ? OR org_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)", userId, userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	var rows []MonitorRegionStatus
	err = s.client.DB.NewSelect().
		Model(&rows).
		Where("monitor_id = ?", monitorId).
		Order("region ASC").
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	configured := make(map[string]bool, len(m.Regions))
	for _, r := range m.Regions {
		configured[r] = true
	}
	effInterval := models.EffectiveInterval(m.Interval, m.OwnerPlan, m.Timeout)
	staleAfter := time.Duration(effInterval*models.RegionStaleMultiplier) * time.Second

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
		if rows[i].StatusCode != nil {
			code := *rows[i].StatusCode
			st.StatusCode = &code
		}
		if rows[i].Message != nil {
			msg := *rows[i].Message
			st.Message = &msg
		}
		out = append(out, st)
	}
	return out, nil
}

func (s *BunStore) ListIncidents(ctx context.Context, monitorId, userId string, limit int) ([]models.Incident, error) {
	var m Monitor
	err := s.client.DB.NewSelect().
		Model(&m).
		Where("id = ?", monitorId).
		Where("user_id = ? OR org_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)", userId, userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	var incidents []Incident
	err = s.client.DB.NewSelect().
		Model(&incidents).
		Where("monitor_id = ?", monitorId).
		Order("started_at DESC").
		Limit(limit).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	out := make([]models.Incident, len(incidents))
	for i, inc := range incidents {
		out[i] = models.Incident{
			ID:        inc.ID,
			MonitorID: inc.MonitorID,
			Status:    models.Status(inc.Status),
			StartedAt: inc.StartedAt,
		}
		if inc.ResolvedAt != nil {
			r := *inc.ResolvedAt
			out[i].ResolvedAt = &r
			d := r.Sub(inc.StartedAt).Milliseconds()
			out[i].DurationMs = &d
		}
		if inc.StatusCode != nil {
			code := *inc.StatusCode
			out[i].StatusCode = &code
		}
		if inc.Message != nil {
			msg := *inc.Message
			out[i].Message = &msg
		}
	}
	return out, nil
}

func (s *BunStore) GetMonitorStats(ctx context.Context, monitorId, userId string, since time.Time) (*models.MonitorStats, error) {
	var m Monitor
	err := s.client.DB.NewSelect().
		Model(&m).
		Where("id = ?", monitorId).
		Where("user_id = ? OR org_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)", userId, userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	until := time.Now()
	var stats *models.MonitorStats

	if until.Sub(since) <= rawStatsWindow {
		var rs []MonitorResult
		err = s.client.DB.NewSelect().
			Model(&rs).
			Where("monitor_id = ?", monitorId).
			Where("checked_at >= ?", since).
			Order("checked_at ASC").
			Scan(ctx)
		if err != nil {
			return nil, mapError(err)
		}
		stats = computeStats(rs, since, until)

		for _, region := range resultRegions(rs) {
			var group []MonitorResult
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
		var rus []MonitorResultRollup
		err = s.client.DB.NewSelect().
			Model(&rus).
			Where("monitor_id = ?", monitorId).
			Where("bucket >= ?", since.Truncate(time.Hour)).
			Order("bucket ASC").
			Scan(ctx)
		if err != nil {
			return nil, mapError(err)
		}
		rows := make([]rollupRow, len(rus))
		for i := range rus {
			rows[i] = rollupRow{
				Region: rus[i].Region,
				Bucket: rus[i].Bucket,
				Checks: rus[i].Checks,
				// The rollup's latency columns cover UP + DEGRADED checks only,
				// so those two counts are the latency sample count.
				LatencyChecks: rus[i].UpChecks + rus[i].DegradedChecks,
				SumLatency:    rus[i].SumLatency,
				MinLatency:    rus[i].MinLatency,
				MaxLatency:    rus[i].MaxLatency,
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

	var incidents []Incident
	err = s.client.DB.NewSelect().
		Model(&incidents).
		Where("monitor_id = ?", monitorId).
		Where("started_at >= ?", since).
		Order("started_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	stats.IncidentCount = len(incidents)
	stats.MTBFSeconds = meanTimeBetweenFailures(incidents)

	return stats, nil
}

func (s *BunStore) GetMonitorUptime(ctx context.Context, monitorId, userId string, days, tzOffsetMinutes int) (*models.MonitorUptime, error) {
	var m Monitor
	err := s.client.DB.NewSelect().
		Model(&m).
		Where("id = ?", monitorId).
		Where("user_id = ? OR org_id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)", userId, userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	// Work in the caller's calendar days, carried as UTC wall-clock dates:
	// shift now into their offset and truncate to get "today", then shift the
	// window start back to a real UTC instant for the bucket filter.
	// Rollups are hourly, so offsets that aren't whole hours (IST, Nepal,
	// Chatham) land day boundaries up to 45 minutes off — cheaper to accept
	// than to split buckets.
	loc := time.FixedZone("client", tzOffsetMinutes*60)
	nowLocal := time.Now().In(loc)
	today := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, time.UTC)
	since := today.AddDate(0, 0, -(days - 1)).Add(-time.Duration(tzOffsetMinutes) * time.Minute)

	var rows []dailyUptimeRow
	err = s.client.DB.NewSelect().
		Model((*MonitorResultRollup)(nil)).
		ColumnExpr("date_trunc('day', bucket + make_interval(mins => ?)) AS day", tzOffsetMinutes).
		ColumnExpr("region").
		ColumnExpr("sum(up_checks + degraded_checks) AS available").
		ColumnExpr("sum(checks) AS total").
		Where("monitor_id = ?", monitorId).
		Where("bucket >= ?", since).
		GroupExpr("1, 2").
		Scan(ctx, &rows)
	if err != nil {
		return nil, mapError(err)
	}

	series, uptime7d := computeDailyUptime(rows, today, days)
	return &models.MonitorUptime{Days: series, Uptime7d: uptime7d}, nil
}

// meanTimeBetweenFailures averages the recovery-to-next-failure gaps between
// consecutive incidents (ordered by started_at). Pairs whose earlier incident
// is still open contribute no gap; nil when no pair qualifies.
func meanTimeBetweenFailures(incidents []Incident) *int64 {
	var totalSecs int64
	var gaps int64
	for i := 1; i < len(incidents); i++ {
		prev := incidents[i-1]
		if prev.ResolvedAt == nil {
			continue
		}
		gap := incidents[i].StartedAt.Sub(*prev.ResolvedAt)
		if gap < 0 {
			gap = 0
		}
		totalSecs += int64(gap.Seconds())
		gaps++
	}
	if gaps == 0 {
		return nil
	}
	mean := totalSecs / gaps
	return &mean
}

// GetMonitorExpiryStatus returns the monitor's CERT/DOMAIN sub-check rows —
// whichever exist. A sub-check that has never run (never enabled, or enabled
// but not yet claimed by the scheduler's expiry sweep) has no row.
func (s *BunStore) GetMonitorExpiryStatus(ctx context.Context, monitorId string) ([]models.MonitorExpiryStatus, error) {
	var rows []MonitorExpiryStatus
	err := s.client.DB.NewSelect().
		Model(&rows).
		Where("monitor_id = ?", monitorId).
		Order("kind ASC").
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	now := time.Now()
	out := make([]models.MonitorExpiryStatus, len(rows))
	for i, r := range rows {
		var lastErr *string
		if r.LastError != nil {
			e := *r.LastError
			lastErr = &e
		}
		var daysLeft *int
		if r.ExpiresAt != nil {
			d := models.ExpiryDaysLeft(*r.ExpiresAt, now)
			daysLeft = &d
		}
		out[i] = models.MonitorExpiryStatus{
			Kind:      models.ExpiryKind(r.Kind),
			ExpiresAt: r.ExpiresAt,
			DaysLeft:  daysLeft,
			CheckedAt: r.CheckedAt,
			LastError: lastErr,
		}
	}
	return out, nil
}

// ── Maintenance windows ──────────────────────────────────────────────────────

// ListMaintenanceWindows returns the monitor's windows. Ownership is the
// caller's problem (handlers resolve it via GetMonitor first).
func (s *BunStore) ListMaintenanceWindows(ctx context.Context, monitorId string) ([]models.MaintenanceWindow, error) {
	var rows []MaintenanceWindow
	err := s.client.DB.NewSelect().
		Model(&rows).
		Where("monitor_id = ?", monitorId).
		Order("created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]models.MaintenanceWindow, len(rows))
	for i := range rows {
		out[i] = rows[i].toModel()
	}
	return out, nil
}

func (s *BunStore) CreateMaintenanceWindow(ctx context.Context, monitorId string, req models.CreateMaintenanceWindowRequest) (*models.MaintenanceWindow, error) {
	w := &MaintenanceWindow{
		ID:              uuid.NewString(),
		MonitorID:       monitorId,
		Kind:            string(req.Kind),
		StartsAt:        req.StartsAt,
		EndsAt:          req.EndsAt,
		Weekday:         req.Weekday,
		StartTime:       req.StartTime,
		DurationMinutes: req.DurationMinutes,
		Timezone:        req.Timezone,
	}
	if err := s.client.DB.NewInsert().Model(w).ExcludeColumn("created_at").Returning("*").Scan(ctx); err != nil {
		return nil, mapError(err)
	}
	model := w.toModel()
	return &model, nil
}

func (s *BunStore) DeleteMaintenanceWindow(ctx context.Context, monitorId, windowId string) error {
	res, err := s.client.DB.NewDelete().
		Model((*MaintenanceWindow)(nil)).
		Where("id = ?", windowId).
		Where("monitor_id = ?", monitorId).
		Exec(ctx)
	if err != nil {
		return mapError(err)
	}
	if rows, err := res.RowsAffected(); err == nil && rows == 0 {
		return models.ErrNotFound
	}
	return nil
}

// ── Notification channels (global, per-user) and per-monitor overrides ─────────

func (s *BunStore) CreateNotificationChannel(ctx context.Context, userId, channel, target string, enabled bool) (*models.NotificationChannel, error) {
	nc := &NotificationChannel{
		ID:        uuid.NewString(),
		UserID:    userId,
		Channel:   channel,
		Target:    target,
		Enabled:   enabled,
		UpdatedAt: time.Now(),
	}
	if err := s.client.DB.NewInsert().Model(nc).ExcludeColumn("created_at").Returning("*").Scan(ctx); err != nil {
		return nil, mapError(err)
	}
	model := nc.toModel()
	return &model, nil
}

func (s *BunStore) ListNotificationChannels(ctx context.Context, userId string) ([]models.NotificationChannel, error) {
	var chs []NotificationChannel
	err := s.client.DB.NewSelect().
		Model(&chs).
		Where("user_id = ?", userId).
		Order("created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]models.NotificationChannel, len(chs))
	for i, nc := range chs {
		out[i] = nc.toModel()
	}
	return out, nil
}

func (s *BunStore) GetNotificationChannel(ctx context.Context, id, userId string) (*models.NotificationChannel, error) {
	var nc NotificationChannel
	err := s.client.DB.NewSelect().
		Model(&nc).
		Where("id = ?", id).
		Where("user_id = ?", userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	model := nc.toModel()
	return &model, nil
}

func (s *BunStore) UpdateNotificationChannel(ctx context.Context, id, userId string, req models.UpdateNotificationChannelRequest) (*models.NotificationChannel, error) {
	var nc NotificationChannel
	err := s.client.DB.NewSelect().
		Model(&nc).
		Where("id = ?", id).
		Where("user_id = ?", userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	q := s.client.DB.NewUpdate().
		Model(&nc).
		Where("id = ?", id)

	var hasUpdates bool
	if req.Channel != nil {
		nc.Channel = string(*req.Channel)
		q = q.Set("channel = ?", string(*req.Channel))
		hasUpdates = true
	}
	if req.Target != nil {
		nc.Target = *req.Target
		q = q.Set("target = ?", *req.Target)
		hasUpdates = true
	}
	if req.Enabled != nil {
		nc.Enabled = *req.Enabled
		q = q.Set("enabled = ?", *req.Enabled)
		hasUpdates = true
	}

	if hasUpdates {
		nc.UpdatedAt = time.Now()
		q = q.Set("updated_at = ?", nc.UpdatedAt)
		if _, err := q.Exec(ctx); err != nil {
			return nil, mapError(err)
		}
	}

	model := nc.toModel()
	return &model, nil
}

func (s *BunStore) DeleteNotificationChannel(ctx context.Context, id, userId string) error {
	var nc NotificationChannel
	err := s.client.DB.NewSelect().
		Model(&nc).
		Where("id = ?", id).
		Where("user_id = ?", userId).
		Scan(ctx)
	if err != nil {
		return mapError(err)
	}

	_, err = s.client.DB.NewDelete().
		Model((*NotificationChannel)(nil)).
		Where("id = ?", id).
		Exec(ctx)
	return mapError(err)
}

func (s *BunStore) ListMonitorChannelSettings(ctx context.Context, monitorId string) ([]models.MonitorChannelSetting, error) {
	var settings []MonitorChannelSetting
	err := s.client.DB.NewSelect().
		Model(&settings).
		Where("monitor_id = ?", monitorId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]models.MonitorChannelSetting, len(settings))
	for i, mcs := range settings {
		out[i] = models.MonitorChannelSetting{
			ID:                    mcs.ID,
			MonitorID:             mcs.MonitorID,
			NotificationChannelID: mcs.NotificationChannelID,
			Enabled:               mcs.Enabled,
		}
	}
	return out, nil
}

func (s *BunStore) UpsertMonitorChannelSetting(ctx context.Context, monitorId, channelId string, enabled bool) (*models.MonitorChannelSetting, error) {
	mcs := &MonitorChannelSetting{
		ID:                    uuid.NewString(),
		MonitorID:             monitorId,
		NotificationChannelID: channelId,
		Enabled:               enabled,
		UpdatedAt:             time.Now(),
	}
	err := s.client.DB.NewInsert().
		Model(mcs).
		ExcludeColumn("created_at").
		On("CONFLICT (monitor_id, notification_channel_id) DO UPDATE").
		Set("enabled = EXCLUDED.enabled").
		Set("updated_at = NOW()").
		Returning("*").
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := &models.MonitorChannelSetting{
		ID:                    mcs.ID,
		MonitorID:             mcs.MonitorID,
		NotificationChannelID: mcs.NotificationChannelID,
		Enabled:               mcs.Enabled,
	}
	return out, nil
}

func (s *BunStore) DeleteMonitorChannelSetting(ctx context.Context, monitorId, channelId string) error {
	_, err := s.client.DB.NewDelete().
		Model((*MonitorChannelSetting)(nil)).
		Where("monitor_id = ? AND notification_channel_id = ?", monitorId, channelId).
		Exec(ctx)
	return mapError(err)
}

// ── Organizations ────────────────────────────────────────────────────────────

func (s *BunStore) CreateOrganization(ctx context.Context, userId, name string) (*models.Organization, error) {
	var org Organization
	var member OrganizationMember

	err := s.client.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		org = Organization{
			ID:        uuid.NewString(),
			Name:      name,
			OwnerID:   userId,
			UpdatedAt: time.Now(),
		}
		if err := tx.NewInsert().Model(&org).ExcludeColumn("created_at").Returning("*").Scan(ctx); err != nil {
			return err
		}

		member = OrganizationMember{
			ID:             uuid.NewString(),
			OrganizationID: org.ID,
			UserID:         userId,
			Role:           "OWNER",
		}
		if err := tx.NewInsert().Model(&member).ExcludeColumn("created_at").Returning("*").Scan(ctx); err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		return nil, mapError(err)
	}

	model := org.toModel()
	return &model, nil
}

func (s *BunStore) GetOrganization(ctx context.Context, id string) (*models.Organization, error) {
	var org Organization
	err := s.client.DB.NewSelect().
		Model(&org).
		Where("id = ?", id).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	model := org.toModel()
	return &model, nil
}

func (s *BunStore) ListOrganizations(ctx context.Context, userId string) ([]models.Organization, error) {
	var orgs []Organization
	err := s.client.DB.NewSelect().
		Model(&orgs).
		Where("id IN (SELECT organization_id FROM organization_members WHERE user_id = ?)", userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]models.Organization, len(orgs))
	for i, o := range orgs {
		out[i] = o.toModel()
	}
	return out, nil
}

func (s *BunStore) UpdateOrganization(ctx context.Context, id string, req models.UpdateOrgRequest) (*models.Organization, error) {
	if req.Name == nil {
		return nil, models.ErrNotFound
	}

	var org Organization
	err := s.client.DB.NewSelect().
		Model(&org).
		Where("id = ?", id).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	org.Name = *req.Name
	org.UpdatedAt = time.Now()

	_, err = s.client.DB.NewUpdate().
		Model(&org).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	model := org.toModel()
	return &model, nil
}

func (s *BunStore) DeleteOrganization(ctx context.Context, id string) error {
	res, err := s.client.DB.NewDelete().
		Model((*Organization)(nil)).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return mapError(err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return models.ErrNotFound
	}
	return nil
}

// ── Members ──────────────────────────────────────────────────────────────────

func (s *BunStore) GetMembership(ctx context.Context, orgId, userId string) (*models.OrganizationMember, error) {
	var om OrganizationMember
	err := s.client.DB.NewSelect().
		Model(&om).
		Where("organization_id = ? AND user_id = ?", orgId, userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	model := om.toModel()
	return &model, nil
}

func (s *BunStore) ListMembers(ctx context.Context, orgId string) ([]models.OrganizationMember, error) {
	var members []OrganizationMember
	err := s.client.DB.NewSelect().
		Model(&members).
		Where("organization_id = ?", orgId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]models.OrganizationMember, len(members))
	for i, om := range members {
		out[i] = om.toModel()
	}
	return out, nil
}

func (s *BunStore) UpdateMemberRole(ctx context.Context, orgId, userId string, role models.OrgRole) (*models.OrganizationMember, error) {
	var om OrganizationMember
	err := s.client.DB.NewSelect().
		Model(&om).
		Where("organization_id = ? AND user_id = ?", orgId, userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	om.Role = string(role)
	_, err = s.client.DB.NewUpdate().
		Model(&om).
		Where("id = ?", om.ID).
		Exec(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	model := om.toModel()
	return &model, nil
}

func (s *BunStore) RemoveMember(ctx context.Context, orgId, userId string) error {
	var om OrganizationMember
	err := s.client.DB.NewSelect().
		Model(&om).
		Where("organization_id = ? AND user_id = ?", orgId, userId).
		Scan(ctx)
	if err != nil {
		return mapError(err)
	}

	_, err = s.client.DB.NewDelete().
		Model((*OrganizationMember)(nil)).
		Where("id = ?", om.ID).
		Exec(ctx)
	return mapError(err)
}

// CountNonOwnerMembers counts the members holding a login seat: everyone but
// the OWNER, who is free.
func (s *BunStore) CountNonOwnerMembers(ctx context.Context, orgId string) (int, error) {
	count, err := s.client.DB.NewSelect().
		Model((*OrganizationMember)(nil)).
		Where("organization_id = ? AND role <> 'OWNER'", orgId).
		Count(ctx)
	return count, mapError(err)
}

// ── Org alert recipients ─────────────────────────────────────────────────────

func (s *BunStore) CreateOrgAlertRecipient(ctx context.Context, orgId, channel, target string) (*models.OrgAlertRecipient, error) {
	r := &OrgAlertRecipient{
		ID:             uuid.NewString(),
		OrganizationID: orgId,
		Channel:        channel,
		Target:         target,
	}
	if err := s.client.DB.NewInsert().Model(r).ExcludeColumn("created_at").Returning("*").Scan(ctx); err != nil {
		return nil, mapError(err)
	}
	model := r.toModel()
	return &model, nil
}

func (s *BunStore) ListOrgAlertRecipients(ctx context.Context, orgId string) ([]models.OrgAlertRecipient, error) {
	var recipients []OrgAlertRecipient
	err := s.client.DB.NewSelect().
		Model(&recipients).
		Where("organization_id = ?", orgId).
		Order("created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]models.OrgAlertRecipient, len(recipients))
	for i, r := range recipients {
		out[i] = r.toModel()
	}
	return out, nil
}

func (s *BunStore) CountOrgAlertRecipients(ctx context.Context, orgId string) (int, error) {
	count, err := s.client.DB.NewSelect().
		Model((*OrgAlertRecipient)(nil)).
		Where("organization_id = ?", orgId).
		Count(ctx)
	return count, mapError(err)
}

func (s *BunStore) DeleteOrgAlertRecipient(ctx context.Context, orgId, id string) error {
	res, err := s.client.DB.NewDelete().
		Model((*OrgAlertRecipient)(nil)).
		Where("id = ? AND organization_id = ?", id, orgId).
		Exec(ctx)
	if err != nil {
		return mapError(err)
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return models.ErrNotFound
	}
	return nil
}

// ── Invitations ──────────────────────────────────────────────────────────────

func (s *BunStore) CreateInvitation(ctx context.Context, orgId, email, invitedBy string, role models.OrgRole, token string, expiresAt time.Time) (*models.Invitation, error) {
	inv := &Invitation{
		ID:             uuid.NewString(),
		OrganizationID: orgId,
		Email:          email,
		Role:           string(role),
		Token:          token,
		Status:         "PENDING",
		InvitedBy:      invitedBy,
		ExpiresAt:      expiresAt,
	}
	if err := s.client.DB.NewInsert().Model(inv).ExcludeColumn("created_at").Returning("*").Scan(ctx); err != nil {
		return nil, mapError(err)
	}
	model := inv.toModel()
	return &model, nil
}

func (s *BunStore) GetInvitationByToken(ctx context.Context, token string) (*models.Invitation, error) {
	var inv Invitation
	err := s.client.DB.NewSelect().
		Model(&inv).
		Where("token = ?", token).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	model := inv.toModel()
	return &model, nil
}

func (s *BunStore) GetInvitationByID(ctx context.Context, id string) (*models.Invitation, error) {
	var inv Invitation
	err := s.client.DB.NewSelect().
		Model(&inv).
		Where("id = ?", id).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	model := inv.toModel()
	return &model, nil
}

func (s *BunStore) ListInvitations(ctx context.Context, orgId string) ([]models.Invitation, error) {
	var invitations []Invitation
	err := s.client.DB.NewSelect().
		Model(&invitations).
		Where("organization_id = ?", orgId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]models.Invitation, len(invitations))
	for i, inv := range invitations {
		out[i] = inv.toModel()
	}
	return out, nil
}

// CountPendingInvitations counts PENDING, non-expired invitations — each one
// holds a login seat until accepted, revoked, or expired.
func (s *BunStore) CountPendingInvitations(ctx context.Context, orgId string) (int, error) {
	count, err := s.client.DB.NewSelect().
		Model((*Invitation)(nil)).
		Where("organization_id = ? AND status = 'PENDING' AND expires_at > now()", orgId).
		Count(ctx)
	return count, mapError(err)
}

func (s *BunStore) AcceptInvitation(ctx context.Context, token, userId string, maxLoginSeats int) (*models.OrganizationMember, error) {
	var member OrganizationMember

	err := s.client.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var inv Invitation
		err := tx.NewSelect().
			Model(&inv).
			Where("token = ?", token).
			Scan(ctx)
		if err != nil {
			return err
		}

		// Authoritative login-seat check. Lock the org row so concurrent
		// accepts serialize, then count seats in use, excluding this
		// invitation — it already holds the seat it is converting.
		if maxLoginSeats != models.Unlimited {
			var orgID string
			if err := tx.NewSelect().
				Table("organizations").
				Column("id").
				Where("id = ?", inv.OrganizationID).
				For("UPDATE").
				Scan(ctx, &orgID); err != nil {
				return err
			}

			var seatsUsed int
			if err := tx.NewRaw(`
				SELECT (SELECT count(*) FROM organization_members
				         WHERE organization_id = ? AND role <> 'OWNER')
				     + (SELECT count(*) FROM invitations
				         WHERE organization_id = ? AND status = 'PENDING'
				           AND expires_at > now() AND id <> ?)`,
				inv.OrganizationID, inv.OrganizationID, inv.ID,
			).Scan(ctx, &seatsUsed); err != nil {
				return err
			}
			if seatsUsed >= maxLoginSeats {
				return models.ErrSeatLimit
			}
		}

		member = OrganizationMember{
			ID:             uuid.NewString(),
			OrganizationID: inv.OrganizationID,
			UserID:         userId,
			Role:           inv.Role,
		}
		if err := tx.NewInsert().Model(&member).ExcludeColumn("created_at").Returning("*").Scan(ctx); err != nil {
			return err
		}

		inv.Status = "ACCEPTED"
		if _, err := tx.NewUpdate().
			Model(&inv).
			Where("id = ?", inv.ID).
			Set("status = ?", "ACCEPTED").
			Exec(ctx); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return nil, mapError(err)
	}

	model := member.toModel()
	return &model, nil
}

func (s *BunStore) RevokeInvitation(ctx context.Context, id string) error {
	var inv Invitation
	err := s.client.DB.NewSelect().
		Model(&inv).
		Where("id = ?", id).
		Scan(ctx)
	if err != nil {
		return mapError(err)
	}

	inv.Status = "REVOKED"
	_, err = s.client.DB.NewUpdate().
		Model(&inv).
		Where("id = ?", id).
		Set("status = ?", "REVOKED").
		Exec(ctx)
	return mapError(err)
}

// ── Subscriptions ────────────────────────────────────────────────────────────

func (s *BunStore) GetSubscriptionByUser(ctx context.Context, userId string) (*models.Subscription, error) {
	var sub Subscription
	err := s.client.DB.NewSelect().
		Model(&sub).
		Where("user_id = ?", userId).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	model := sub.toModel()
	return &model, nil
}

func (s *BunStore) GetSubscriptionByCustomerID(ctx context.Context, customerID string) (*models.Subscription, error) {
	var sub Subscription
	err := s.client.DB.NewSelect().
		Model(&sub).
		Where("stripe_customer_id = ?", customerID).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	model := sub.toModel()
	return &model, nil
}

func (s *BunStore) UpsertSubscription(ctx context.Context, params models.UpsertSubscriptionParams) (*models.Subscription, error) {
	sub := &Subscription{
		ID:                   uuid.NewString(),
		UserID:               params.UserID,
		Plan:                 params.Plan,
		Status:               params.Status,
		StripeCustomerID:     params.StripeCustomerID,
		StripeSubscriptionID: params.StripeSubscriptionID,
		StripePriceID:        params.StripePriceID,
		CurrentPeriodStart:   params.CurrentPeriodStart,
		CurrentPeriodEnd:     params.CurrentPeriodEnd,
		UpdatedAt:            time.Now(),
	}

	err := s.client.DB.NewInsert().
		Model(sub).
		ExcludeColumn("created_at").
		On("CONFLICT (user_id) DO UPDATE").
		Set("plan = EXCLUDED.plan").
		Set("status = EXCLUDED.status").
		Set("stripe_customer_id = EXCLUDED.stripe_customer_id").
		Set("stripe_subscription_id = EXCLUDED.stripe_subscription_id").
		Set("stripe_price_id = EXCLUDED.stripe_price_id").
		Set("current_period_start = EXCLUDED.current_period_start").
		Set("current_period_end = EXCLUDED.current_period_end").
		Set("updated_at = NOW()").
		Returning("*").
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}

	model := sub.toModel()
	return &model, nil
}

func (s *BunStore) ReconcileMonitorsToPlan(ctx context.Context, userId, oldPlan, newPlan string) (int, error) {
	oldLimits := models.LimitsForPlan(oldPlan)
	newLimits := models.LimitsForPlan(newPlan)
	total := 0

	planScopeSQL := `((user_id = ? AND org_id IS NULL) OR org_id IN (SELECT id FROM organizations WHERE owner_id = ?))`

	// Follow-plan monitors (interval IS NULL) re-resolve their interval at read
	// time, so a plan change needs no write for them — the upgrade re-grant is
	// gone entirely. Only explicit overrides still need clamping, and only on a
	// downgrade: an override below the new floor is raised to it. (An override
	// is never lowered on upgrade — the user chose that value deliberately.)
	if newLimits.MinInterval > oldLimits.MinInterval {
		query := fmt.Sprintf(
			`UPDATE monitors SET interval = ?, updated_at = now()
			 WHERE %s AND interval IS NOT NULL AND interval < ?`,
			planScopeSQL,
		)
		res, err := s.client.DB.NewRaw(query, newLimits.MinInterval, userId, userId, newLimits.MinInterval).Exec(ctx)
		if err != nil {
			return total, mapError(err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return total, mapError(err)
		}
		total += int(rows)
	}

	if newLimits.MaxRegions != models.Unlimited {
		query := fmt.Sprintf(
			`UPDATE monitors SET regions = regions[1:(?::int)], updated_at = now()
			 WHERE %s AND cardinality(regions) > ?`,
			planScopeSQL,
		)
		res, err := s.client.DB.NewRaw(query, newLimits.MaxRegions, userId, userId, newLimits.MaxRegions).Exec(ctx)
		if err != nil {
			return total, mapError(err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return total, mapError(err)
		}
		total += int(rows)

		if rows > 0 {
			deleteQuery := `DELETE FROM monitor_region_status mrs
				  USING monitors m
				  WHERE mrs.monitor_id = m.id
				    AND ((m.user_id = ? AND m.org_id IS NULL)
				         OR m.org_id IN (SELECT id FROM organizations WHERE owner_id = ?))
				    AND NOT (mrs.region = ANY(m.regions))`
			if _, err := s.client.DB.NewRaw(deleteQuery, userId, userId).Exec(ctx); err != nil {
				log.Printf("Failed to clean up region status rows for user %s: %v", userId, err)
			}
		}
	}

	// Custom slow-response thresholds are plan-gated: once the effective plan
	// loses the capability, overrides revert to the per-type default.
	if oldLimits.CustomDegradedThreshold && !newLimits.CustomDegradedThreshold {
		query := fmt.Sprintf(
			`UPDATE monitors SET degraded_threshold_ms = NULL, updated_at = now()
			 WHERE %s AND degraded_threshold_ms IS NOT NULL`,
			planScopeSQL,
		)
		res, err := s.client.DB.NewRaw(query, userId, userId).Exec(ctx)
		if err != nil {
			return total, mapError(err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return total, mapError(err)
		}
		total += int(rows)
	}

	// Expiry monitoring flags are plan-gated the same way: losing the
	// capability switches the sub-checks off (thresholds keep their values —
	// harmless without the flag, and preserved across a round-trip).
	if (oldLimits.SSLMonitoring && !newLimits.SSLMonitoring) ||
		(oldLimits.DomainMonitoring && !newLimits.DomainMonitoring) {
		query := fmt.Sprintf(
			`UPDATE monitors SET cert_check_enabled = false, domain_check_enabled = false, updated_at = now()
			 WHERE %s AND (cert_check_enabled OR domain_check_enabled)`,
			planScopeSQL,
		)
		res, err := s.client.DB.NewRaw(query, userId, userId).Exec(ctx)
		if err != nil {
			return total, mapError(err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return total, mapError(err)
		}
		total += int(rows)
	}

	// Repeat alerts are plan-gated the same way: losing the capability clears
	// the config so downgraded accounts stop getting reminder sends.
	if oldLimits.MaxAlertRepeats > 0 && newLimits.MaxAlertRepeats == 0 {
		query := fmt.Sprintf(
			`UPDATE monitors
			    SET repeat_alert_interval_secs = NULL, repeat_alert_max_count = NULL, updated_at = now()
			  WHERE %s AND repeat_alert_interval_secs IS NOT NULL`,
			planScopeSQL,
		)
		res, err := s.client.DB.NewRaw(query, userId, userId).Exec(ctx)
		if err != nil {
			return total, mapError(err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return total, mapError(err)
		}
		total += int(rows)
	}

	return total, nil
}

// ── Stats calculation helpers ────────────────────────────────────────────────

const statBuckets = 48
const rawStatsWindow = 25 * time.Hour

type rollupRow struct {
	Region string
	Bucket time.Time
	// Checks counts every check in the bucket; LatencyChecks counts only the
	// ones that produced a response (UP + DEGRADED) and so is the divisor for
	// SumLatency. They differ whenever the bucket holds a DOWN check, whose
	// "latency" is a timeout, not a round trip — see migration
	// 20260909120000_latency_excludes_down.
	Checks        int
	LatencyChecks int
	SumLatency    int
	MinLatency    int
	MaxLatency    int
}

// measuredLatency reports whether a check of this status actually timed a round
// trip. A DOWN check's recorded latency is how long the checker waited before
// giving up — a timeout, not a response time — so it is left out of every
// latency aggregate. Kept in sync with the `status <> 'DOWN'` FILTER in
// migration 20260909120000_latency_excludes_down, which applies the same rule
// to the hourly rollups.
func measuredLatency(s models.Status) bool { return s != models.StatusDOWN }

func computeStats(rs []MonitorResult, since, until time.Time) *models.MonitorStats {
	stats := &models.MonitorStats{Points: []models.StatPoint{}}
	if len(rs) == 0 {
		return stats
	}

	// Latency aggregates come from responsive checks only. A DOWN row's
	// latency is the time the checker waited before giving up, so counting it
	// would report a 30s ping timeout as a 30000ms response time. TotalChecks
	// still counts every check.
	min, max, sum, count := 0, 0, 0, 0
	for i := range rs {
		if !measuredLatency(models.Status(rs[i].Status)) {
			continue
		}
		l := rs[i].Latency
		if count == 0 || l < min {
			min = l
		}
		if count == 0 || l > max {
			max = l
		}
		sum += l
		count++
	}
	stats.TotalChecks = len(rs)
	if count > 0 {
		stats.MinLatency = min
		stats.MaxLatency = max
		stats.AvgLatency = float64(sum) / float64(count)
	}

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
		if !measuredLatency(models.Status(rs[i].Status)) {
			continue
		}
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

func resultRegions(rs []MonitorResult) []string {
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

func computeStatsFromRollups(rows []rollupRow, since, until time.Time) *models.MonitorStats {
	stats := &models.MonitorStats{Points: []models.StatPoint{}}
	if len(rows) == 0 {
		return stats
	}

	// Rows with no responsive check hold no latency (the rollup writes 0s
	// there), so they contribute to TotalChecks but never to min/max/avg.
	min, max, totalSum, latencyCount, totalCount := 0, 0, 0, 0, 0
	for i := range rows {
		totalCount += rows[i].Checks
		if rows[i].LatencyChecks == 0 {
			continue
		}
		if latencyCount == 0 || rows[i].MinLatency < min {
			min = rows[i].MinLatency
		}
		if latencyCount == 0 || rows[i].MaxLatency > max {
			max = rows[i].MaxLatency
		}
		totalSum += rows[i].SumLatency
		latencyCount += rows[i].LatencyChecks
	}
	if totalCount == 0 {
		return stats
	}
	stats.TotalChecks = totalCount
	if latencyCount > 0 {
		stats.MinLatency = min
		stats.MaxLatency = max
		stats.AvgLatency = float64(totalSum) / float64(latencyCount)
	}

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
		if rows[i].LatencyChecks == 0 {
			continue
		}
		idx := int(rows[i].Bucket.Sub(since) / bucketDur)
		if idx < 0 {
			idx = 0
		}
		if idx >= statBuckets {
			idx = statBuckets - 1
		}
		buckets[idx].sum += rows[i].SumLatency
		buckets[idx].count += rows[i].LatencyChecks
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

// ── Uptime calculation helpers ───────────────────────────────────────────────

const dayFormat = "2006-01-02"

// dailyUptimeRow is one (local calendar day, region) group of the uptime
// query. Available counts UP and DEGRADED checks: degraded means reachable but
// slow, which still counts as available.
type dailyUptimeRow struct {
	Day       time.Time `bun:"day"`
	Region    string    `bun:"region"`
	Available int       `bun:"available"`
	Total     int       `bun:"total"`
}

// computeDailyUptime shapes the grouped rows into exactly `days` entries ending
// at `today`, oldest-first, and returns the trailing 7-day figure alongside.
// Days with no rows are emitted as not-monitored rather than as an outage.
// `today` is a calendar date carried in UTC; rows must be bucketed against the
// same offset.
func computeDailyUptime(rows []dailyUptimeRow, today time.Time, days int) ([]models.DailyUptime, float64) {
	type acc struct{ available, total int }

	byDay := make(map[string]map[string]*acc)
	for _, r := range rows {
		key := r.Day.Format(dayFormat)
		regions, ok := byDay[key]
		if !ok {
			regions = make(map[string]*acc)
			byDay[key] = regions
		}
		a, ok := regions[r.Region]
		if !ok {
			a = &acc{}
			regions[r.Region] = a
		}
		a.available += r.Available
		a.total += r.Total
	}

	// Region totals over the trailing 7 days, accumulated as we walk.
	sevenDay := make(map[string]*acc)

	out := make([]models.DailyUptime, 0, days)
	for i := days - 1; i >= 0; i-- {
		day := models.DailyUptime{Date: today.AddDate(0, 0, -i).Format(dayFormat)}

		var sum float64
		var reporting int
		for region, a := range byDay[day.Date] {
			if a.total == 0 {
				continue
			}
			// Average each region's share of available checks rather than
			// pooling raw counts, so a fast-checking region can't outvote a
			// slower one by sheer volume.
			sum += float64(a.available) / float64(a.total) * 100
			reporting++

			if i < 7 {
				s, ok := sevenDay[region]
				if !ok {
					s = &acc{}
					sevenDay[region] = s
				}
				s.available += a.available
				s.total += a.total
			}
		}
		if reporting > 0 {
			pct := sum / float64(reporting)
			day.Monitored = true
			day.UptimePercent = &pct
		}
		out = append(out, day)
	}

	// Check-weighted within each region, then averaged across regions — an
	// average of the seven daily percentages would weight a quiet day the same
	// as a busy one.
	var sum float64
	var reporting int
	for _, a := range sevenDay {
		if a.total == 0 {
			continue
		}
		sum += float64(a.available) / float64(a.total) * 100
		reporting++
	}
	var uptime7d float64
	if reporting > 0 {
		uptime7d = sum / float64(reporting)
	}

	return out, uptime7d
}
