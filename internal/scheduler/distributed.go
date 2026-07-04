package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/coordination"
	"upguardly-backend/internal/models"
)

type DistributedScheduler struct {
	store        models.SchedulerStore
	region       string
	runner       *checkRunner
	coordinator  *coordination.Coordinator
	partitions   *coordination.PartitionManager
	syncInterval time.Duration

	jobs     map[string]*job
	mu       sync.RWMutex
	stopCh   chan struct{}
	stopOnce sync.Once
	doneCh   chan struct{}
}

func NewDistributedScheduler(
	store models.SchedulerStore,
	alertManager *alerter.Manager,
	coordinator *coordination.Coordinator,
	partitions *coordination.PartitionManager,
	syncInterval time.Duration,
	region string,
) *DistributedScheduler {
	return &DistributedScheduler{
		store:        store,
		region:       region,
		runner:       newCheckRunner(store, alertManager, region),
		coordinator:  coordinator,
		partitions:   partitions,
		syncInterval: syncInterval,
		jobs:         make(map[string]*job),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

func (s *DistributedScheduler) Start(ctx context.Context) error {
	eventCh := make(chan coordination.MembershipEvent, 10)
	if err := s.coordinator.WatchInstances(ctx, eventCh); err != nil {
		return err
	}

	go s.run(ctx, eventCh)

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
			log.Printf("Membership change: %d instances", len(event.Instances))
			s.applyOwnership(event.Instances)
			s.syncMonitors(ctx)
		case <-ticker.C:
			// Periodic backstop: re-list membership from etcd so a missed or
			// coalesced watch event can never leave ownership stale for more
			// than one sync interval.
			if instances, err := s.coordinator.Reconcile(ctx); err != nil {
				log.Printf("Warning: failed to reconcile membership: %v", err)
			} else {
				s.applyOwnership(instances)
			}
			s.syncMonitors(ctx)
		}
	}
}

// applyOwnership recalculates partition ownership from the given membership
// and stops jobs for partitions this instance no longer owns. It is a no-op
// when ownership is unchanged. This instance may legitimately be absent from
// the list (e.g. its lease expired before the keepalive re-registered); in
// that case it owns nothing until it reappears.
func (s *DistributedScheduler) applyOwnership(instances []string) {
	delta := s.partitions.RecalculateOwnership(instances)
	if len(delta.Lost) == 0 && len(delta.Gained) == 0 {
		return
	}

	if len(delta.Lost) > 0 {
		log.Printf("Lost %d partitions: %v", len(delta.Lost), delta.Lost)
		s.stopJobsForPartitions(delta.Lost)
	}

	if len(delta.Gained) > 0 {
		log.Printf("Gained %d partitions: %v", len(delta.Gained), delta.Gained)
	}

	log.Printf("Now owning %d partitions", len(s.partitions.GetOwnedPartitions()))
}

func (s *DistributedScheduler) stopJobsForPartitions(partitions []int) {
	partitionSet := make(map[int]bool)
	for _, p := range partitions {
		partitionSet[p] = true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for monitorID, j := range s.jobs {
		partition := s.partitions.CalculatePartition(monitorID)
		if partitionSet[partition] {
			j.cancel()
			delete(s.jobs, monitorID)
			log.Printf("Stopped job for monitor %s (partition %d no longer owned)", monitorID, partition)
		}
	}
}

// fetchOwnedMonitors returns the enabled monitors that list this scheduler's
// region and fall in partitions this instance owns. Both filters run in SQL
// (see PartitionSQLExpr), so each instance's sync cost scales with the
// monitors it owns rather than every instance scanning the entire table.
func (s *DistributedScheduler) fetchOwnedMonitors(ctx context.Context) ([]models.Monitor, error) {
	owned := s.partitions.GetOwnedPartitions()
	if len(owned) == 0 {
		return nil, nil
	}

	// Owning everything (e.g. a single instance): a plain query is cheaper.
	if len(owned) == s.partitions.PartitionCount() {
		return s.store.FetchActiveMonitors(ctx, s.region)
	}

	return s.store.FetchOwnedMonitors(ctx, s.region, owned, s.partitions.PartitionSQLExpr())
}

func (s *DistributedScheduler) syncMonitors(ctx context.Context) {
	monitors, err := s.fetchOwnedMonitors(ctx)
	if err != nil {
		log.Printf("Failed to fetch monitors: %v", err)
		return
	}

	activeIDs := make(map[string]bool)

	for _, m := range monitors {
		// Belt and suspenders: the query already filters by owned partition,
		// but ownership may have changed while the query ran.
		if !s.partitions.IsMonitorOwned(m.ID) {
			continue
		}

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
func (s *DistributedScheduler) reconcileJob(ctx context.Context, m *models.Monitor) {
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

func (s *DistributedScheduler) startMonitorJob(parentCtx context.Context, m *models.Monitor) {
	ctx, cancel := context.WithCancel(parentCtx)

	s.mu.Lock()
	s.jobs[m.ID] = &job{cancel: cancel, updatedAt: m.UpdatedAt}
	s.mu.Unlock()

	go s.runner.jobLoop(ctx, m)

	log.Printf("Started monitoring job for %s (%s) on partition %d", m.Name, m.ID, s.partitions.CalculatePartition(m.ID))
}

func (s *DistributedScheduler) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })

	select {
	case <-s.doneCh:
	case <-time.After(10 * time.Second):
		log.Printf("Timeout waiting for scheduler to stop")
	}

	s.mu.Lock()
	for id, j := range s.jobs {
		j.cancel()
		delete(s.jobs, id)
	}
	s.mu.Unlock()

	s.runner.stop()
}

func (s *DistributedScheduler) GetActiveJobCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.jobs)
}

func (s *DistributedScheduler) GetOwnedPartitions() []int {
	return s.partitions.GetOwnedPartitions()
}
