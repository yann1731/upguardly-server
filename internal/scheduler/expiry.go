package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"upguardly-backend/internal/models"
	"upguardly-backend/internal/monitor"
)

// expirySweepInterval paces the cert/domain expiry sweep. Each sub-check is
// only re-checked every 24h (see ClaimDueExpiryChecks), so an hourly tick is
// frequent enough to spread the daily load without meaningfully delaying any
// single check.
const (
	expirySweepInterval = time.Hour
	expiryBatchSize     = 50
	expiryCheckTimeout  = 15 * time.Second
)

// expirySweeper periodically checks due SSL-certificate and domain-expiry
// sub-checks (ENTERPRISE, HTTP monitors only) and records the outcome.
// Claiming a batch (ClaimDueExpiryChecks) advances checked_at under SKIP
// LOCKED, so concurrent scheduler instances can't double-check; bucket state
// and alert fan-out live in RecordExpiryCheck.
type expirySweeper struct {
	store  models.SchedulerStore
	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

func newExpirySweeper(store models.SchedulerStore) *expirySweeper {
	s := &expirySweeper{
		store:  store,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	go s.loop()
	return s
}

func (s *expirySweeper) stop() {
	s.once.Do(func() { close(s.stopCh) })
	select {
	case <-s.doneCh:
	case <-time.After(10 * time.Second):
		log.Printf("Timeout waiting for expiry sweeper to stop")
	}
}

func (s *expirySweeper) loop() {
	defer close(s.doneCh)

	ticker := time.NewTicker(expirySweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			// A full batch may mean there's a backlog: keep draining until a
			// batch comes back short, same pattern as the alert dispatcher.
			for s.sweepBatch() == expiryBatchSize {
				select {
				case <-s.stopCh:
					return
				default:
				}
			}
		}
	}
}

func (s *expirySweeper) sweepBatch() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	claims, err := s.store.ClaimDueExpiryChecks(ctx, expiryBatchSize)
	if err != nil {
		log.Printf("Failed to claim expiry checks: %v", err)
		return 0
	}

	for _, claim := range claims {
		s.runOne(ctx, claim)
	}
	return len(claims)
}

func (s *expirySweeper) runOne(ctx context.Context, claim models.ExpiryCheckClaim) {
	var expiresAt *time.Time
	var checkErr string

	switch claim.Kind {
	case models.ExpiryKindCert:
		t, err := monitor.CheckCertExpiry(ctx, claim.Target, expiryCheckTimeout)
		if err != nil {
			checkErr = err.Error()
		} else {
			expiresAt = &t
		}
	case models.ExpiryKindDomain:
		t, err := monitor.CheckDomainExpiry(ctx, claim.Target, expiryCheckTimeout)
		if err != nil {
			checkErr = err.Error()
		} else {
			expiresAt = &t
		}
	default:
		log.Printf("Unknown expiry check kind %q for monitor %s", claim.Kind, claim.MonitorID)
		return
	}

	if err := s.store.RecordExpiryCheck(ctx, claim, expiresAt, checkErr); err != nil {
		log.Printf("Failed to record %s expiry check for monitor %s: %v", claim.Kind, claim.MonitorID, err)
	}
}
