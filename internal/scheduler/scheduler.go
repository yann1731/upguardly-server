package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/database"
	db "upguardly-backend/internal/database/prisma"
)

// job tracks one running monitor goroutine. updatedAt is the monitor's
// updated_at at the time the job was started: when it changes, the scheduler
// restarts the job so interval/target/timeout edits take effect without any
// per-tick config reads.
type job struct {
	cancel    context.CancelFunc
	updatedAt time.Time
}

// Scheduler is the embedded single-process scheduler used by cmd/server for
// one-box deployments. It checks every enabled monitor in-process.
type Scheduler struct {
	db       *database.Client
	runner   *checkRunner
	jobs     map[string]*job
	mu       sync.RWMutex
	stopCh   chan struct{}
	stopOnce sync.Once
}

func NewScheduler(dbc *database.Client, alertManager *alerter.Manager) *Scheduler {
	return &Scheduler{
		db:     dbc,
		runner: newCheckRunner(dbc, alertManager),
		jobs:   make(map[string]*job),
		stopCh: make(chan struct{}),
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
		case <-s.stopCh:
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
		s.reconcileJob(ctx, &m)
	}

	s.mu.Lock()
	for id, j := range s.jobs {
		if !activeIDs[id] {
			j.cancel()
			delete(s.jobs, id)
		}
	}
	s.mu.Unlock()
}

// reconcileJob ensures a job is running for m with its current config,
// restarting it when the monitor row changed since the job started.
func (s *Scheduler) reconcileJob(ctx context.Context, m *db.MonitorModel) {
	s.mu.RLock()
	j, exists := s.jobs[m.ID]
	s.mu.RUnlock()

	if exists {
		if j.updatedAt.Equal(m.UpdatedAt) {
			return
		}
		j.cancel()
		log.Printf("Monitor %s config changed, restarting job", m.ID)
	}

	s.startMonitorJob(ctx, m)
}

func (s *Scheduler) startMonitorJob(parentCtx context.Context, m *db.MonitorModel) {
	ctx, cancel := context.WithCancel(parentCtx)

	s.mu.Lock()
	s.jobs[m.ID] = &job{cancel: cancel, updatedAt: m.UpdatedAt}
	s.mu.Unlock()

	go s.runner.jobLoop(ctx, m)

	log.Printf("Started monitoring job for %s (%s)", m.Name, m.ID)
}

func (s *Scheduler) Stop() {
	// Stop the sync loop first so it cannot recreate jobs after they are
	// canceled below (Stop must work even if the Start context is not
	// canceled by the caller).
	s.stopOnce.Do(func() { close(s.stopCh) })

	s.mu.Lock()
	for id, j := range s.jobs {
		j.cancel()
		delete(s.jobs, id)
	}
	s.mu.Unlock()

	s.runner.stop()
}
