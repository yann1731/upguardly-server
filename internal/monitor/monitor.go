package monitor

import (
	"context"
	"time"

	"upguardly-backend/internal/models"
)

type Checker interface {
	Check(ctx context.Context, target string, timeout time.Duration) models.CheckResult
}

func NewChecker(monitorType models.MonitorType) Checker {
	switch monitorType {
	case models.MonitorTypeHTTP:
		return &HTTPChecker{}
	case models.MonitorTypePORT:
		return &PortChecker{}
	case models.MonitorTypePING:
		return &PingChecker{}
	default:
		return nil
	}
}
