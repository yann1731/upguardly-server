package bun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

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

func (s *BunStore) CreateMonitor(ctx context.Context, userId, orgId, name, monitorType, target string, interval *int, timeout int, enabled bool, regions []string) (*models.Monitor, error) {
	var orgIDPtr *string
	if orgId != "" {
		orgIDPtr = &orgId
	}
	m := &Monitor{
		UserID:   userId,
		OrgID:    orgIDPtr,
		Name:     name,
		Type:     monitorType,
		Target:   target,
		Interval: interval,
		Timeout:  timeout,
		Enabled:  enabled,
		Regions:  regions,
	}
	if err := s.client.DB.NewInsert().Model(m).ExcludeColumn("id", "created_at", "updated_at", "owner_plan").Returning("*").Scan(ctx); err != nil {
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
	if req.Enabled != nil {
		m.Enabled = *req.Enabled
		q = q.Set("enabled = ?", *req.Enabled)
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

	var incidents []Incident
	err = s.client.DB.NewSelect().
		Model(&incidents).
		Where("monitor_id = ?", monitorId).
		Where("started_at >= ?", since).
		Scan(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	stats.IncidentCount = len(incidents)

	return stats, nil
}

// ── Notification channels (global, per-user) and per-monitor overrides ─────────

func (s *BunStore) CreateNotificationChannel(ctx context.Context, userId, channel, target string, enabled bool) (*models.NotificationChannel, error) {
	nc := &NotificationChannel{
		UserID:  userId,
		Channel: channel,
		Target:  target,
		Enabled: enabled,
	}
	if err := s.client.DB.NewInsert().Model(nc).ExcludeColumn("id", "created_at", "updated_at").Returning("*").Scan(ctx); err != nil {
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

func (s *BunStore) CountNotificationChannels(ctx context.Context, userId string) (int, error) {
	count, err := s.client.DB.NewSelect().
		Model((*NotificationChannel)(nil)).
		Where("user_id = ?", userId).
		Count(ctx)
	return count, mapError(err)
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
		MonitorID:             monitorId,
		NotificationChannelID: channelId,
		Enabled:               enabled,
	}
	err := s.client.DB.NewInsert().
		Model(mcs).
		ExcludeColumn("id", "created_at", "updated_at").
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
			Name:    name,
			OwnerID: userId,
		}
		if err := tx.NewInsert().Model(&org).ExcludeColumn("id", "created_at", "updated_at").Returning("*").Scan(ctx); err != nil {
			return err
		}

		member = OrganizationMember{
			OrganizationID: org.ID,
			UserID:         userId,
			Role:           "OWNER",
		}
		if err := tx.NewInsert().Model(&member).ExcludeColumn("id", "created_at").Returning("*").Scan(ctx); err != nil {
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

// ── Invitations ──────────────────────────────────────────────────────────────

func (s *BunStore) CreateInvitation(ctx context.Context, orgId, email, invitedBy string, role models.OrgRole, token string, expiresAt time.Time) (*models.Invitation, error) {
	inv := &Invitation{
		OrganizationID: orgId,
		Email:          email,
		Role:           string(role),
		Token:          token,
		Status:         "PENDING",
		InvitedBy:      invitedBy,
		ExpiresAt:      expiresAt,
	}
	if err := s.client.DB.NewInsert().Model(inv).ExcludeColumn("id", "created_at").Returning("*").Scan(ctx); err != nil {
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

func (s *BunStore) AcceptInvitation(ctx context.Context, token, userId string) (*models.OrganizationMember, error) {
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

		member = OrganizationMember{
			OrganizationID: inv.OrganizationID,
			UserID:         userId,
			Role:           inv.Role,
		}
		if err := tx.NewInsert().Model(&member).ExcludeColumn("id", "created_at").Returning("*").Scan(ctx); err != nil {
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

func (s *BunStore) UpsertSubscription(ctx context.Context, params models.UpsertSubscriptionParams) (*models.Subscription, error) {
	sub := &Subscription{
		UserID:               params.UserID,
		Plan:                 params.Plan,
		Status:               params.Status,
		StripeCustomerID:     params.StripeCustomerID,
		StripeSubscriptionID: params.StripeSubscriptionID,
		StripePriceID:        params.StripePriceID,
		CurrentPeriodStart:   params.CurrentPeriodStart,
		CurrentPeriodEnd:     params.CurrentPeriodEnd,
	}

	err := s.client.DB.NewInsert().
		Model(sub).
		ExcludeColumn("id", "created_at", "updated_at").
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

	return total, nil
}

// ── Stats calculation helpers ────────────────────────────────────────────────

const statBuckets = 48
const rawStatsWindow = 25 * time.Hour

type rollupRow struct {
	Region     string
	Bucket     time.Time
	Checks     int
	SumLatency int
	MinLatency int
	MaxLatency int
}

func computeStats(rs []MonitorResult, since, until time.Time) *models.MonitorStats {
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
