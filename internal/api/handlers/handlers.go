package handlers

import (
	"sync"
	"time"

	"upguardly-backend/internal/mailer"
	"upguardly-backend/internal/models"
)

// reconcileTTL bounds how often GetSubscription reconciles a user's
// subscription against live Stripe state. Reconciliation exists to heal
// records after dropped webhooks; doing it on every read added a synchronous
// Stripe round trip per billing-page view and let page traffic burn Stripe's
// API rate limit. Within the TTL the DB record is served as-is. The cache is
// per-process: with N replicas the worst case is N reconciles per user per
// TTL, which is still bounded.
const reconcileTTL = time.Minute

type Handlers struct {
	store  models.Store
	mailer *mailer.Mailer
	stripe StripeService

	reconcileMu sync.Mutex
	reconciled  map[string]time.Time
}

func NewHandlers(store models.Store, m *mailer.Mailer, s StripeService) *Handlers {
	return &Handlers{
		store:      store,
		mailer:     m,
		stripe:     s,
		reconciled: make(map[string]time.Time),
	}
}

// shouldReconcile reports whether the user's subscription is due for a live
// Stripe reconcile, and if so records the attempt so concurrent and follow-up
// requests within the TTL skip it.
func (h *Handlers) shouldReconcile(userID string) bool {
	now := time.Now()

	h.reconcileMu.Lock()
	defer h.reconcileMu.Unlock()

	if last, ok := h.reconciled[userID]; ok && now.Sub(last) < reconcileTTL {
		return false
	}

	// Opportunistically drop expired entries so the map doesn't grow with
	// every user ever seen. Amortized: only when the map is large.
	if len(h.reconciled) > 10000 {
		for id, t := range h.reconciled {
			if now.Sub(t) >= reconcileTTL {
				delete(h.reconciled, id)
			}
		}
	}

	h.reconciled[userID] = now
	return true
}

// forgetReconcile clears the user's reconcile timestamp so the next
// GetSubscription reflects live Stripe state immediately. Called after
// billing actions (checkout, cancel) whose effects the user expects to see
// on the very next read.
func (h *Handlers) forgetReconcile(userID string) {
	h.reconcileMu.Lock()
	defer h.reconcileMu.Unlock()
	delete(h.reconciled, userID)
}
