package alerter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"upguardly-backend/internal/models"
)

// TestManagerSendConcurrentTargets verifies that a single shared Manager can
// deliver alerts to different targets concurrently without crossing wires.
// Before the fix, the Manager mutated a shared alerter's destination field per
// call, so concurrent sends could race and deliver to the wrong target.
//
// Run with: go test -race ./internal/alerter/...
func TestManagerSendConcurrentTargets(t *testing.T) {
	// Two webhook servers standing in for two distinct Slack destinations.
	var hitsA, hitsB int64
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hitsA, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hitsB, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srvB.Close()

	m := &Manager{
		alerters: map[models.AlertChannel]Alerter{
			models.AlertChannelSLACK: NewSlackAlerter(),
		},
	}

	monitor := &models.Monitor{Name: "test", Type: models.MonitorTypeHTTP, Target: "https://example.com"}
	result := &models.CheckResult{Status: models.StatusDOWN, Latency: 1, Message: "down"}

	const perTarget = 100
	var wg sync.WaitGroup
	for i := 0; i < perTarget; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = m.Send(context.Background(), models.AlertChannelSLACK, srvA.URL, monitor, result)
		}()
		go func() {
			defer wg.Done()
			_ = m.Send(context.Background(), models.AlertChannelSLACK, srvB.URL, monitor, result)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&hitsA); got != perTarget {
		t.Errorf("target A: got %d deliveries, want %d", got, perTarget)
	}
	if got := atomic.LoadInt64(&hitsB); got != perTarget {
		t.Errorf("target B: got %d deliveries, want %d", got, perTarget)
	}
}
