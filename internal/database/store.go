package database

import (
	"context"
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

func (s *PrismaStore) CreateMonitor(ctx context.Context, userId, orgId, name, monitorType, target string, interval, timeout int, enabled bool) (*models.Monitor, error) {
	optional := []db.MonitorSetParam{
		db.Monitor.Interval.Set(interval),
		db.Monitor.Timeout.Set(timeout),
		db.Monitor.Enabled.Set(enabled),
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
		db.Monitor.UserID.Equals(userId),
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
		db.Monitor.UserID.Equals(userId),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	return monitorToModel(m), nil
}

func (s *PrismaStore) UpdateMonitor(ctx context.Context, id, userId string, req models.UpdateMonitorRequest) (*models.Monitor, error) {
	if _, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(id),
		db.Monitor.UserID.Equals(userId),
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

	m, err := s.client.Prisma.Monitor.FindUnique(
		db.Monitor.ID.Equals(id),
	).Update(params...).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return monitorToModel(m), nil
}

func (s *PrismaStore) DeleteMonitor(ctx context.Context, id, userId string) error {
	if _, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(id),
		db.Monitor.UserID.Equals(userId),
	).Exec(ctx); err != nil {
		return models.ErrNotFound
	}
	_, err := s.client.Prisma.Monitor.FindUnique(
		db.Monitor.ID.Equals(id),
	).Delete().Exec(ctx)
	return err
}

func (s *PrismaStore) GetMonitorResults(ctx context.Context, monitorId, userId string, limit int) ([]models.MonitorResult, error) {
	if _, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(monitorId),
		db.Monitor.UserID.Equals(userId),
	).Exec(ctx); err != nil {
		return nil, models.ErrNotFound
	}

	rs, err := s.client.Prisma.MonitorResult.FindMany(
		db.MonitorResult.MonitorID.Equals(monitorId),
	).OrderBy(
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

// ── Incidents & stats ──────────────────────────────────────────────────────────

func (s *PrismaStore) ListIncidents(ctx context.Context, monitorId, userId string, limit int) ([]models.Incident, error) {
	if _, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(monitorId),
		db.Monitor.UserID.Equals(userId),
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
		db.Monitor.UserID.Equals(userId),
	).Exec(ctx); err != nil {
		return nil, models.ErrNotFound
	}

	rs, err := s.client.Prisma.MonitorResult.FindMany(
		db.MonitorResult.MonitorID.Equals(monitorId),
		db.MonitorResult.CheckedAt.Gte(since),
	).OrderBy(
		db.MonitorResult.CheckedAt.Order(db.ASC),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}

	stats := computeStats(rs, since, time.Now())

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

// ── Alert ────────────────────────────────────────────────────────────────────

func (s *PrismaStore) CreateAlert(ctx context.Context, monitorId, userId, channel, target string, enabled bool) (*models.Alert, error) {
	if _, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(monitorId),
		db.Monitor.UserID.Equals(userId),
	).Exec(ctx); err != nil {
		return nil, models.ErrNotFound
	}

	a, err := s.client.Prisma.Alert.CreateOne(
		db.Alert.Monitor.Link(db.Monitor.ID.Equals(monitorId)),
		db.Alert.Channel.Set(db.AlertChannel(channel)),
		db.Alert.Target.Set(target),
		db.Alert.Enabled.Set(enabled),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return alertToModel(a), nil
}

func (s *PrismaStore) ListAlerts(ctx context.Context, monitorId, userId string) ([]models.Alert, error) {
	if _, err := s.client.Prisma.Monitor.FindFirst(
		db.Monitor.ID.Equals(monitorId),
		db.Monitor.UserID.Equals(userId),
	).Exec(ctx); err != nil {
		return nil, models.ErrNotFound
	}

	as, err := s.client.Prisma.Alert.FindMany(
		db.Alert.MonitorID.Equals(monitorId),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]models.Alert, len(as))
	for i := range as {
		out[i] = *alertToModel(&as[i])
	}
	return out, nil
}

func (s *PrismaStore) GetAlert(ctx context.Context, id string) (*models.Alert, error) {
	a, err := s.client.Prisma.Alert.FindUnique(
		db.Alert.ID.Equals(id),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	return alertToModel(a), nil
}

func (s *PrismaStore) UpdateAlert(ctx context.Context, id string, req models.UpdateAlertRequest) (*models.Alert, error) {
	var params []db.AlertSetParam
	if req.Channel != nil {
		params = append(params, db.Alert.Channel.Set(db.AlertChannel(*req.Channel)))
	}
	if req.Target != nil {
		params = append(params, db.Alert.Target.Set(*req.Target))
	}
	if req.Enabled != nil {
		params = append(params, db.Alert.Enabled.Set(*req.Enabled))
	}

	a, err := s.client.Prisma.Alert.FindUnique(
		db.Alert.ID.Equals(id),
	).Update(params...).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return alertToModel(a), nil
}

func (s *PrismaStore) DeleteAlert(ctx context.Context, id string) error {
	_, err := s.client.Prisma.Alert.FindUnique(
		db.Alert.ID.Equals(id),
	).Delete().Exec(ctx)
	return err
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
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func alertToModel(a *db.AlertModel) *models.Alert {
	return &models.Alert{
		ID:        a.ID,
		MonitorID: a.MonitorID,
		Channel:   models.AlertChannel(a.Channel),
		Target:    a.Target,
		Enabled:   a.Enabled,
		CreatedAt: a.CreatedAt,
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
