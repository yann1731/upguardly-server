package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/database"
	db "upguardly-backend/internal/database/prisma"
	"upguardly-backend/internal/models"
	"upguardly-backend/internal/monitor"
)

type Scheduler struct {
	db           *database.Client
	alertManager *alerter.Manager
	jobs         map[string]context.CancelFunc
	mu           sync.RWMutex
	lastStatus   map[string]models.Status
}

func NewScheduler(db *database.Client, alertManager *alerter.Manager) *Scheduler {
	return &Scheduler{
		db:           db,
		alertManager: alertManager,
		jobs:         make(map[string]context.CancelFunc),
		lastStatus:   make(map[string]models.Status),
	}
}

func (s *Scheduler) Start(ctx context.Context) error {
	go s.syncLoop(ctx)
	return nil
}

func (s *Scheduler) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	s.syncMonitors(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncMonitors(ctx)
		}
	}
}

func (s *Scheduler) syncMonitors(ctx context.Context) {
	monitors, err := s.db.Prisma.Monitor.FindMany(
		db.Monitor.Enabled.Equals(true),
	).Exec(ctx)

	if err != nil {
		log.Printf("Failed to fetch monitors: %v", err)
		return
	}

	activeIDs := make(map[string]bool)

	for _, m := range monitors {
		activeIDs[m.ID] = true

		s.mu.RLock()
		_, exists := s.jobs[m.ID]
		s.mu.RUnlock()

		if !exists {
			s.startMonitorJob(ctx, &m)
		}
	}

	s.mu.Lock()
	for id, cancel := range s.jobs {
		if !activeIDs[id] {
			cancel()
			delete(s.jobs, id)
			delete(s.lastStatus, id)
		}
	}
	s.mu.Unlock()
}

func (s *Scheduler) startMonitorJob(parentCtx context.Context, m *db.MonitorModel) {
	ctx, cancel := context.WithCancel(parentCtx)

	s.mu.Lock()
	s.jobs[m.ID] = cancel
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(time.Duration(m.Interval) * time.Second)
		defer ticker.Stop()

		s.runCheck(ctx, m)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				freshMonitor, err := s.db.Prisma.Monitor.FindUnique(
					db.Monitor.ID.Equals(m.ID),
				).Exec(ctx)
				if err != nil {
					continue
				}
				s.runCheck(ctx, freshMonitor)
			}
		}
	}()

	log.Printf("Started monitoring job for %s (%s)", m.Name, m.ID)
}

func (s *Scheduler) runCheck(ctx context.Context, m *db.MonitorModel) {
	checker := monitor.NewChecker(models.MonitorType(m.Type))
	if checker == nil {
		log.Printf("Unknown monitor type: %s", m.Type)
		return
	}

	timeout := time.Duration(m.Timeout) * time.Second
	result := checker.Check(ctx, m.Target, timeout)

	s.saveResult(ctx, m.ID, &result)

	s.mu.RLock()
	lastStatus, hasLast := s.lastStatus[m.ID]
	s.mu.RUnlock()

	if !hasLast || lastStatus != result.Status {
		s.mu.Lock()
		s.lastStatus[m.ID] = result.Status
		s.mu.Unlock()

		if hasLast {
			s.sendAlerts(ctx, m, &result)
		}
	}

	log.Printf("Monitor %s: %s (latency: %dms)", m.Name, result.Status, result.Latency)
}

func (s *Scheduler) saveResult(ctx context.Context, monitorID string, result *models.CheckResult) {
	var optionalParams []db.MonitorResultSetParam

	optionalParams = append(optionalParams, db.MonitorResult.Message.Set(result.Message))

	if result.StatusCode != nil {
		optionalParams = append(optionalParams, db.MonitorResult.StatusCode.Set(*result.StatusCode))
	}

	_, err := s.db.Prisma.MonitorResult.CreateOne(
		db.MonitorResult.Monitor.Link(db.Monitor.ID.Equals(monitorID)),
		db.MonitorResult.Status.Set(db.Status(result.Status)),
		db.MonitorResult.Latency.Set(result.Latency),
		optionalParams...,
	).Exec(ctx)

	if err != nil {
		log.Printf("Failed to save monitor result: %v", err)
	}
}

func (s *Scheduler) sendAlerts(ctx context.Context, m *db.MonitorModel, result *models.CheckResult) {
	alerts, err := s.db.Prisma.Alert.FindMany(
		db.Alert.MonitorID.Equals(m.ID),
		db.Alert.Enabled.Equals(true),
	).Exec(ctx)

	if err != nil {
		log.Printf("Failed to fetch alerts: %v", err)
		return
	}

	mon := &models.Monitor{
		ID:     m.ID,
		Name:   m.Name,
		Type:   models.MonitorType(m.Type),
		Target: m.Target,
	}

	for _, alert := range alerts {
		err := s.alertManager.Send(ctx, models.AlertChannel(alert.Channel), alert.Target, mon, result)
		if err != nil {
			log.Printf("Failed to send %s alert: %v", alert.Channel, err)
		} else {
			log.Printf("Sent %s alert for %s", alert.Channel, m.Name)
		}

		s.saveAlertHistory(ctx, alert.ID, result)
	}
}

func (s *Scheduler) saveAlertHistory(ctx context.Context, alertID string, result *models.CheckResult) {
	_, err := s.db.Prisma.AlertHistory.CreateOne(
		db.AlertHistory.Alert.Link(db.Alert.ID.Equals(alertID)),
		db.AlertHistory.Status.Set(db.Status(result.Status)),
		db.AlertHistory.Message.Set(result.Message),
	).Exec(ctx)

	if err != nil {
		log.Printf("Failed to save alert history: %v", err)
	}
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, cancel := range s.jobs {
		cancel()
		delete(s.jobs, id)
	}
}
