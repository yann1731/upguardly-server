package database

import (
	"context"

	"upguardly-backend/internal/models"

	db "upguardly-backend/internal/database/prisma"
)

// ── NotificationChannel (global, per-user) ────────────────────────────────────

func (s *PrismaStore) CreateNotificationChannel(ctx context.Context, userId, channel, target string, enabled bool) (*models.NotificationChannel, error) {
	ch, err := s.client.Prisma.NotificationChannel.CreateOne(
		db.NotificationChannel.UserID.Set(userId),
		db.NotificationChannel.Channel.Set(db.AlertChannel(channel)),
		db.NotificationChannel.Target.Set(target),
		db.NotificationChannel.Enabled.Set(enabled),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return notificationChannelToModel(ch), nil
}

func (s *PrismaStore) ListNotificationChannels(ctx context.Context, userId string) ([]models.NotificationChannel, error) {
	chs, err := s.client.Prisma.NotificationChannel.FindMany(
		db.NotificationChannel.UserID.Equals(userId),
	).OrderBy(db.NotificationChannel.CreatedAt.Order(db.ASC)).Exec(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]models.NotificationChannel, len(chs))
	for i := range chs {
		out[i] = *notificationChannelToModel(&chs[i])
	}
	return out, nil
}

func (s *PrismaStore) CountNotificationChannels(ctx context.Context, userId string) (int, error) {
	chs, err := s.client.Prisma.NotificationChannel.FindMany(
		db.NotificationChannel.UserID.Equals(userId),
	).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return len(chs), nil
}

func (s *PrismaStore) GetNotificationChannel(ctx context.Context, id, userId string) (*models.NotificationChannel, error) {
	ch, err := s.client.Prisma.NotificationChannel.FindFirst(
		db.NotificationChannel.ID.Equals(id),
		db.NotificationChannel.UserID.Equals(userId),
	).Exec(ctx)
	if err != nil {
		return nil, models.ErrNotFound
	}
	return notificationChannelToModel(ch), nil
}

func (s *PrismaStore) UpdateNotificationChannel(ctx context.Context, id, userId string, req models.UpdateNotificationChannelRequest) (*models.NotificationChannel, error) {
	// Ownership check first: Update on FindUnique(id) alone would let any user
	// mutate any channel by id.
	if _, err := s.GetNotificationChannel(ctx, id, userId); err != nil {
		return nil, err
	}

	var params []db.NotificationChannelSetParam
	if req.Channel != nil {
		params = append(params, db.NotificationChannel.Channel.Set(db.AlertChannel(*req.Channel)))
	}
	if req.Target != nil {
		params = append(params, db.NotificationChannel.Target.Set(*req.Target))
	}
	if req.Enabled != nil {
		params = append(params, db.NotificationChannel.Enabled.Set(*req.Enabled))
	}

	ch, err := s.client.Prisma.NotificationChannel.FindUnique(
		db.NotificationChannel.ID.Equals(id),
	).Update(params...).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return notificationChannelToModel(ch), nil
}

func (s *PrismaStore) DeleteNotificationChannel(ctx context.Context, id, userId string) error {
	if _, err := s.GetNotificationChannel(ctx, id, userId); err != nil {
		return err
	}
	_, err := s.client.Prisma.NotificationChannel.FindUnique(
		db.NotificationChannel.ID.Equals(id),
	).Delete().Exec(ctx)
	return err
}

// ── MonitorChannelSetting (per-monitor override) ──────────────────────────────

func (s *PrismaStore) ListMonitorChannelSettings(ctx context.Context, monitorId string) ([]models.MonitorChannelSetting, error) {
	rows, err := s.client.Prisma.MonitorChannelSetting.FindMany(
		db.MonitorChannelSetting.MonitorID.Equals(monitorId),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]models.MonitorChannelSetting, len(rows))
	for i := range rows {
		out[i] = *monitorChannelSettingToModel(&rows[i])
	}
	return out, nil
}

func (s *PrismaStore) UpsertMonitorChannelSetting(ctx context.Context, monitorId, channelId string, enabled bool) (*models.MonitorChannelSetting, error) {
	row, err := s.client.Prisma.MonitorChannelSetting.UpsertOne(
		db.MonitorChannelSetting.MonitorIDNotificationChannelID(
			db.MonitorChannelSetting.MonitorID.Equals(monitorId),
			db.MonitorChannelSetting.NotificationChannelID.Equals(channelId),
		),
	).Create(
		db.MonitorChannelSetting.Monitor.Link(db.Monitor.ID.Equals(monitorId)),
		db.MonitorChannelSetting.NotificationChannel.Link(db.NotificationChannel.ID.Equals(channelId)),
		db.MonitorChannelSetting.Enabled.Set(enabled),
	).Update(
		db.MonitorChannelSetting.Enabled.Set(enabled),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	return monitorChannelSettingToModel(row), nil
}

func (s *PrismaStore) DeleteMonitorChannelSetting(ctx context.Context, monitorId, channelId string) error {
	// DeleteMany so that removing a non-existent override (already inherited)
	// is a no-op rather than an error.
	_, err := s.client.Prisma.MonitorChannelSetting.FindMany(
		db.MonitorChannelSetting.MonitorID.Equals(monitorId),
		db.MonitorChannelSetting.NotificationChannelID.Equals(channelId),
	).Delete().Exec(ctx)
	return err
}

// ── Conversion helpers ────────────────────────────────────────────────────────

func notificationChannelToModel(ch *db.NotificationChannelModel) *models.NotificationChannel {
	return &models.NotificationChannel{
		ID:        ch.ID,
		Channel:   models.AlertChannel(ch.Channel),
		Target:    ch.Target,
		Enabled:   ch.Enabled,
		CreatedAt: ch.CreatedAt,
	}
}

func monitorChannelSettingToModel(row *db.MonitorChannelSettingModel) *models.MonitorChannelSetting {
	return &models.MonitorChannelSetting{
		ID:                    row.ID,
		MonitorID:             row.MonitorID,
		NotificationChannelID: row.NotificationChannelID,
		Enabled:               row.Enabled,
	}
}
