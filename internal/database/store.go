package database

import (
	"context"

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

func (s *PrismaStore) CreateMonitor(ctx context.Context, userId, name, monitorType, target string, interval, timeout int, enabled bool) (*models.Monitor, error) {
	m, err := s.client.Prisma.Monitor.CreateOne(
		db.Monitor.Name.Set(name),
		db.Monitor.Type.Set(db.MonitorType(monitorType)),
		db.Monitor.Target.Set(target),
		db.Monitor.UserID.Set(userId),
		db.Monitor.Interval.Set(interval),
		db.Monitor.Timeout.Set(timeout),
		db.Monitor.Enabled.Set(enabled),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return monitorToModel(m), nil
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
	return &models.Monitor{
		ID:        m.ID,
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
