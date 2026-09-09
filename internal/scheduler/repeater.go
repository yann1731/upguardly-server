package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"upguardly-backend/internal/models"
)

// repeatSweepInterval paces the repeat-alert sweep. The minimum configurable
// repeat interval is 300s, so a 30s poll keeps reminder timing within 10% of
// what the user asked for without meaningful load (one indexed query over
// open incidents per tick).
const repeatSweepInterval = 30 * time.Second

// alertRepeater periodically re-enqueues alerts for open incidents whose
// monitor has repeat alerts configured (ENTERPRISE). All state logic —
// due-ness, max-count, maintenance-window suppression, multi-instance safety —
// lives in maintenance.enqueue_repeat_alerts; this loop just ticks it.
type alertRepeater struct {
	store  models.SchedulerStore
	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

func newAlertRepeater(store models.SchedulerStore) *alertRepeater {
	r := &alertRepeater{
		store:  store,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	go r.loop()
	return r
}

func (r *alertRepeater) stop() {
	r.once.Do(func() { close(r.stopCh) })
	select {
	case <-r.doneCh:
	case <-time.After(10 * time.Second):
		log.Printf("Timeout waiting for alert repeater to stop")
	}
}

func (r *alertRepeater) loop() {
	defer close(r.doneCh)

	ticker := time.NewTicker(repeatSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			swept, err := r.store.EnqueueRepeatAlerts(ctx)
			cancel()
			if err != nil {
				log.Printf("Repeat-alert sweep failed: %v", err)
			} else if swept > 0 {
				log.Printf("Repeat-alert sweep re-enqueued alerts for %d incident(s)", swept)
			}
		}
	}
}
