package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"upguardly-backend/internal/metrics"
	"upguardly-backend/internal/models"
	"upguardly-backend/internal/monitor"
)

const (
	// heartbeatInterval is how often this instance marks its region alive.
	// It must stay comfortably below the quorum's active window (60s):
	// KEEP IN SYNC with p_active_threshold in maintenance.evaluate_monitor_quorum.
	heartbeatInterval = 15 * time.Second

	// verifyPollInterval is how often the worker drains confirmation requests
	// and sweeps expired ones. Confirmations should land within seconds of the
	// originating failure, so poll frequently.
	verifyPollInterval = 2 * time.Second

	// verifyBatchSize bounds requests claimed per poll; verifyConcurrency bounds
	// parallel confirmation checks within a batch (a shared-target outage can
	// enqueue one request per affected monitor).
	verifyBatchSize   = 50
	verifyConcurrency = 8
)

// verificationWorker runs the region-local side of cross-region alert
// confirmation: it heartbeats this region so the quorum function knows the
// region is live, drains confirmation requests addressed to this region
// (running a one-off check for each and recording it as a VERIFICATION-source
// result), and sweeps expired requests so a non-responding region can never
// block an alert indefinitely. Both the embedded and distributed schedulers
// get one via checkRunner.
type verificationWorker struct {
	store  models.SchedulerStore
	region string
	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

func newVerificationWorker(store models.SchedulerStore, region string) *verificationWorker {
	w := &verificationWorker{
		store:  store,
		region: region,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	go w.heartbeatLoop()
	go w.verifyLoop()
	return w
}

func (w *verificationWorker) stop() {
	w.once.Do(func() { close(w.stopCh) })
	select {
	case <-w.doneCh:
	case <-time.After(10 * time.Second):
		log.Printf("Timeout waiting for verification worker to stop")
	}
}

func (w *verificationWorker) heartbeatLoop() {
	// Beat immediately so the region is registered as active on startup rather
	// than after the first tick.
	w.beat()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.beat()
		}
	}
}

func (w *verificationWorker) beat() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.store.UpsertRegionHeartbeat(ctx, w.region); err != nil {
		log.Printf("Failed to upsert region heartbeat for %s: %v", w.region, err)
	}
}

func (w *verificationWorker) verifyLoop() {
	defer close(w.doneCh)

	ticker := time.NewTicker(verifyPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
			// Drain a full batch before yielding so a backlog clears fast.
			for w.processBatch() == verifyBatchSize {
				select {
				case <-w.stopCh:
					return
				default:
				}
			}
			w.sweepExpired()
		}
	}
}

// processBatch claims and runs one batch of confirmation checks, returning how
// many it claimed.
func (w *verificationWorker) processBatch() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	reqs, err := w.store.ClaimVerificationRequests(ctx, w.region, verifyBatchSize)
	if err != nil {
		log.Printf("Failed to claim verification requests: %v", err)
		return 0
	}

	sem := make(chan struct{}, verifyConcurrency)
	var wg sync.WaitGroup
	for i := range reqs {
		wg.Add(1)
		sem <- struct{}{}
		go func(req *models.VerificationRequest) {
			defer wg.Done()
			defer func() { <-sem }()
			w.verify(ctx, req)
		}(&reqs[i])
	}
	wg.Wait()

	return len(reqs)
}

func (w *verificationWorker) verify(ctx context.Context, req *models.VerificationRequest) {
	checker := monitor.NewChecker(req.Type)
	if checker == nil {
		log.Printf("Verification: unknown monitor type %s for %s", req.Type, req.MonitorID)
		// Drop the request so it does not sit unclaimable until expiry.
		w.complete(ctx, req.ID)
		return
	}

	timeout := time.Duration(req.Timeout) * time.Second
	result := checker.Check(ctx, req.Target, timeout)

	metrics.VerificationChecksTotal.WithLabelValues(w.region, string(result.Status)).Inc()

	// Record as a VERIFICATION-source check: it counts toward quorum but is
	// deliberately NOT written to monitor_results (one-off checks from regions
	// that don't normally cover this monitor would skew its uptime/latency
	// stats). RecordRegionCheck re-runs quorum, which may open or resolve the
	// incident and enqueue alerts.
	if transition, err := w.store.RecordRegionCheck(ctx, req.MonitorID, w.region, &result, models.SourceVerification); err != nil {
		log.Printf("Verification: failed to record check for %s: %v", req.MonitorID, err)
		return // leave the request; it will be re-claimable via expiry sweep
	} else if transition != "none" {
		log.Printf("Monitor %s: incident %s (region %s confirmed %s)", req.MonitorID, transition, w.region, result.Status)
	}

	w.complete(ctx, req.ID)
}

func (w *verificationWorker) complete(ctx context.Context, id string) {
	if err := w.store.CompleteVerificationRequest(ctx, id); err != nil {
		log.Printf("Verification: failed to complete request %s: %v", id, err)
	}
}

// sweepExpired removes confirmation requests no region answered in time and
// re-evaluates quorum for their monitors, so a genuinely-down region that
// cannot run its own verifier does not stall the alert decision forever. Every
// instance sweeps; SKIP LOCKED keeps them from double-processing, and the
// re-evaluation is idempotent under the per-monitor advisory lock.
func (w *verificationWorker) sweepExpired() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	monitorIDs, err := w.store.ExpireVerificationRequests(ctx)
	if err != nil {
		log.Printf("Failed to expire verification requests: %v", err)
		return
	}
	for _, id := range monitorIDs {
		metrics.VerificationRequestsExpiredTotal.Inc()
		if transition, err := w.store.EvaluateMonitorQuorum(ctx, id); err != nil {
			log.Printf("Failed to re-evaluate quorum for %s after expiry: %v", id, err)
		} else if transition != "none" {
			log.Printf("Monitor %s: incident %s (confirmation window elapsed)", id, transition)
		}
	}
}
