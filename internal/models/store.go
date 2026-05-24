package models

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("not found")

type Store interface {
	CreateMonitor(ctx context.Context, userId, name, monitorType, target string, interval, timeout int, enabled bool) (*Monitor, error)
	ListMonitors(ctx context.Context, userId string) ([]Monitor, error)
	GetMonitor(ctx context.Context, id, userId string) (*Monitor, error)
	UpdateMonitor(ctx context.Context, id, userId string, req UpdateMonitorRequest) (*Monitor, error)
	DeleteMonitor(ctx context.Context, id, userId string) error
	GetMonitorResults(ctx context.Context, monitorId, userId string, limit int) ([]MonitorResult, error)

	CreateAlert(ctx context.Context, monitorId, userId, channel, target string, enabled bool) (*Alert, error)
	ListAlerts(ctx context.Context, monitorId, userId string) ([]Alert, error)
	GetAlert(ctx context.Context, id string) (*Alert, error)
	UpdateAlert(ctx context.Context, id string, req UpdateAlertRequest) (*Alert, error)
	DeleteAlert(ctx context.Context, id string) error
}
