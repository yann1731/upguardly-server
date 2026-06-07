package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/coordination"
	"upguardly-backend/internal/database"
	db "upguardly-backend/internal/database/prisma"
	"upguardly-backend/internal/models"
	"upguardly-backend/internal/monitor"
	"upguardly-backend/internal/statestore"
)

type DistributedScheduler struct {
	db           *database.Client
	alertManager *alerter.Manager
	coordinator  *coordination.Coordinator
	partitions   *coordination.PartitionManager
	stateStore   *statestore.SQLiteStore
	syncInterval time.Duration

	jobs   map[string]context.CancelFunc
	mu     sync.RWMutex
	stopCh chan struct{}
	doneCh chan struct{}
}

func NewDistributedScheduler(
	db *database.Client,
	alertManager *alerter.Manager,
	coordinator *coordination.Coordinator,
	partitions *coordination.PartitionManager,
	stateStore *statestore.SQLiteStore,
	syncInterval time.Duration,
) *DistributedScheduler {
	return &DistributedScheduler{
		db:           db,
		alertManager: alertManager,
		coordinator:  coordinator,
		partitions:   partitions,
		stateStore:   stateStore,
		syncInterval: syncInterval,
		jobs:         make(map[string]context.CancelFunc),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

func (s *DistributedScheduler) Start(ctx context.Context) error {
	if err := s.loadStateFromSQLite(); err != nil {
		log.Printf("Warning: failed to load state from SQLite: %v", err)
	}

	eventCh := make(chan coordination.MembershipEvent, 10)
	if err := s.coordinator.WatchInstances(ctx, eventCh); err != nil {
		return err
	}

	go s.run(ctx, eventCh)

	return nil
}

func (s *DistributedScheduler) loadStateFromSQLite() error {
	state, err := s.stateStore.GetInstanceState()
	if err != nil {
		return err
	}

	if state != nil {
		log.Printf("Recovered state: %d partitions from previous run", len(state.Partitions))
	}

	return nil
}

func (s *DistributedScheduler) run(ctx context.Context, eventCh <-chan coordination.MembershipEvent) {
	defer close(s.doneCh)

	ticker := time.NewTicker(s.syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case event := <-eventCh:
			s.handleMembershipChange(ctx, event)
		case <-ticker.C:
			s.syncMonitors(ctx)
		}
	}
}

func (s *DistributedScheduler) handleMembershipChange(ctx context.Context, event coordination.MembershipEvent) {
	log.Printf("Membership change: %d instances", len(event.Instances))

	delta := s.partitions.RecalculateOwnership(event.Instances)

	if len(delta.Lost) > 0 {
		log.Printf("Lost %d partitions: %v", len(delta.Lost), delta.Lost)
		s.stopJobsForPartitions(delta.Lost)
	}

	if len(delta.Gained) > 0 {
		log.Printf("Gained %d partitions: %v", len(delta.Gained), delta.Gained)
	}

	ownedPartitions := s.partitions.GetOwnedPartitions()
	log.Printf("Now owning %d partitions", len(ownedPartitions))

	if err := s.stateStore.SaveInstanceState(
		s.coordinator.GetInstances()[s.coordinator.GetInstanceIndex()],
		ownedPartitions,
	); err != nil {
		log.Printf("Warning: failed to save instance state: %v", err)
	}

	s.syncMonitors(ctx)
}

func (s *DistributedScheduler) stopJobsForPartitions(partitions []int) {
	partitionSet := make(map[int]bool)
	for _, p := range partitions {
		partitionSet[p] = true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for monitorID, cancel := range s.jobs {
		partition := s.partitions.CalculatePartition(monitorID)
		if partitionSet[partition] {
			cancel()
			delete(s.jobs, monitorID)
			log.Printf("Stopped job for monitor %s (partition %d no longer owned)", monitorID, partition)
		}
	}
}

func (s *DistributedScheduler) syncMonitors(ctx context.Context) {
	monitors, err := s.db.Prisma.Monitor.FindMany(
		db.Monitor.Enabled.Equals(true),
	).Exec(ctx)

	if err != nil {
		log.Printf("Failed to fetch monitors: %v", err)
		return
	}

	activeIDs := make(map[string]bool)

	for _, m := range monitors {
		if !s.partitions.IsMonitorOwned(m.ID) {
			continue
		}

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
			if err := s.stateStore.DeleteMonitorStatus(id); err != nil {
				log.Printf("Warning: failed to delete monitor status: %v", err)
			}
		}
	}
	s.mu.Unlock()
}

func (s *DistributedScheduler) startMonitorJob(parentCtx context.Context, m *db.MonitorModel) {
	ctx, cancel := context.WithCancel(parentCtx)

	s.mu.Lock()
	s.jobs[m.ID] = cancel
	s.mu.Unlock()

	partition := s.partitions.CalculatePartition(m.ID)

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

	log.Printf("Started monitoring job for %s (%s) on partition %d", m.Name, m.ID, partition)
}

func (s *DistributedScheduler) runCheck(ctx context.Context, m *db.MonitorModel) {
	checker := monitor.NewChecker(models.MonitorType(m.Type))
	if checker == nil {
		log.Printf("Unknown monitor type: %s", m.Type)
		return
	}

	timeout := time.Duration(m.Timeout) * time.Second
	result := checker.Check(ctx, m.Target, timeout)

	s.saveResult(ctx, m.ID, &result)
	recordIncident(ctx, s.db, m.ID, &result)

	lastStatus, hasLast, err := s.stateStore.GetLastStatus(m.ID)
	if err != nil {
		log.Printf("Warning: failed to get last status: %v", err)
	}

	if !hasLast || lastStatus != result.Status {
		if err := s.stateStore.SetLastStatus(m.ID, result.Status); err != nil {
			log.Printf("Warning: failed to save status: %v", err)
		}

		if hasLast {
			s.sendAlerts(ctx, m, &result)
		}
	}

	log.Printf("Monitor %s: %s (latency: %dms)", m.Name, result.Status, result.Latency)
}

func (s *DistributedScheduler) saveResult(ctx context.Context, monitorID string, result *models.CheckResult) {
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

func (s *DistributedScheduler) sendAlerts(ctx context.Context, m *db.MonitorModel, result *models.CheckResult) {
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

func (s *DistributedScheduler) saveAlertHistory(ctx context.Context, alertID string, result *models.CheckResult) {
	_, err := s.db.Prisma.AlertHistory.CreateOne(
		db.AlertHistory.Alert.Link(db.Alert.ID.Equals(alertID)),
		db.AlertHistory.Status.Set(db.Status(result.Status)),
		db.AlertHistory.Message.Set(result.Message),
	).Exec(ctx)

	if err != nil {
		log.Printf("Failed to save alert history: %v", err)
	}
}

func (s *DistributedScheduler) Stop() {
	close(s.stopCh)

	select {
	case <-s.doneCh:
	case <-time.After(10 * time.Second):
		log.Printf("Timeout waiting for scheduler to stop")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, cancel := range s.jobs {
		cancel()
		delete(s.jobs, id)
	}
}

func (s *DistributedScheduler) GetActiveJobCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.jobs)
}

func (s *DistributedScheduler) GetOwnedPartitions() []int {
	return s.partitions.GetOwnedPartitions()
}
