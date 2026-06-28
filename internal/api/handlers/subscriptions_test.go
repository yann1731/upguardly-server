package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v76"

	"upguardly-backend/internal/models"
)

func TestGetSubscription(t *testing.T) {
	t.Run("returns stored subscription", func(t *testing.T) {
		store := &mockStore{subResult: aSubscription("PRO")}
		router, h := newOrgRouter(store, nil)
		router.GET("/v1/organizations/:id/subscription", h.GetSubscription)

		w := doRequest(router, "GET", "/v1/organizations/test-org-id/subscription", "")

		assert.Equal(t, http.StatusOK, w.Code)
		var got models.Subscription
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Equal(t, "PRO", got.Plan)
	})

	t.Run("defaults to FREE when none exists", func(t *testing.T) {
		store := &mockStore{} // GetSubscription → ErrNotFound
		router, h := newOrgRouter(store, nil)
		router.GET("/v1/organizations/:id/subscription", h.GetSubscription)

		w := doRequest(router, "GET", "/v1/organizations/test-org-id/subscription", "")

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"plan":"FREE"`)
	})

	t.Run("reconciles a stale record against live Stripe state", func(t *testing.T) {
		// DB shows FREE, but Stripe reports an active PRO subscription.
		sub := aSubscription("FREE")
		cust := "cus_1"
		sub.StripeCustomerID = &cust
		store := &mockStore{subResult: sub}
		fs := &fakeStripe{proPriceID: "price_pro", activeSub: aStripeSub("price_pro", true)}
		router, h := newOrgRouter(store, fs)
		router.GET("/v1/organizations/:id/subscription", h.GetSubscription)

		w := doRequest(router, "GET", "/v1/organizations/test-org-id/subscription", "")

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastUpsertSub)
		assert.Equal(t, "PRO", store.lastUpsertSub.Plan)
	})
}

func TestCancelSubscription(t *testing.T) {
	t.Run("billing not configured returns 503", func(t *testing.T) {
		store := &mockStore{}
		router, h := newOrgRouter(store, nil)
		router.DELETE("/v1/organizations/:id/subscription", h.CancelSubscription)

		w := doRequest(router, "DELETE", "/v1/organizations/test-org-id/subscription", "")

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("no billing subscription returns 404", func(t *testing.T) {
		store := &mockStore{subResult: aSubscription("PRO")} // no StripeSubscriptionID
		router, h := newOrgRouter(store, &fakeStripe{})
		router.DELETE("/v1/organizations/:id/subscription", h.CancelSubscription)

		w := doRequest(router, "DELETE", "/v1/organizations/test-org-id/subscription", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("schedules cancellation at period end", func(t *testing.T) {
		sub := aSubscription("PRO")
		subID := "sub_1"
		sub.StripeSubscriptionID = &subID
		store := &mockStore{subResult: sub}
		fs := &fakeStripe{}
		router, h := newOrgRouter(store, fs)
		router.DELETE("/v1/organizations/:id/subscription", h.CancelSubscription)

		w := doRequest(router, "DELETE", "/v1/organizations/test-org-id/subscription", "")

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, fs.lastCancelAtPeriodEnd)
		assert.True(t, *fs.lastCancelAtPeriodEnd)
		assert.Contains(t, w.Body.String(), `"cancelAtPeriodEnd":true`)
	})
}

// aStripeSub builds a live Stripe subscription with a single line item at the
// given price ID.
func aStripeSub(priceID string, cancelAtPeriodEnd bool) *stripe.Subscription {
	return &stripe.Subscription{
		ID:                "sub_1",
		Status:            stripe.SubscriptionStatusActive,
		CancelAtPeriodEnd: cancelAtPeriodEnd,
		Customer:          &stripe.Customer{ID: "cus_1"},
		CurrentPeriodEnd:  1702592000,
		Items: &stripe.SubscriptionItemList{
			Data: []*stripe.SubscriptionItem{{Price: &stripe.Price{ID: priceID}}},
		},
	}
}

func TestCreateCheckout(t *testing.T) {
	t.Run("billing not configured returns 503", func(t *testing.T) {
		store := &mockStore{}
		router, h := newOrgRouter(store, nil) // nil stripe
		router.POST("/v1/organizations/:id/subscription", h.CreateCheckout)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription", `{"plan":"PRO"}`)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("invalid plan returns 400", func(t *testing.T) {
		store := &mockStore{}
		router, h := newOrgRouter(store, &fakeStripe{})
		router.POST("/v1/organizations/:id/subscription", h.CreateCheckout)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription", `{"plan":"INVALID"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("happy path returns checkout url", func(t *testing.T) {
		store := &mockStore{orgResult: &models.Organization{ID: "test-org-id", Name: "Acme"}}
		fs := &fakeStripe{proPriceID: "price_pro", customerID: "cus_1", checkoutURL: "https://checkout.example/session"}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/organizations/:id/subscription", h.CreateCheckout)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription", `{"plan":"PRO"}`)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "https://checkout.example/session")
	})
}

func TestCreatePortal(t *testing.T) {
	t.Run("billing not configured returns 503", func(t *testing.T) {
		store := &mockStore{}
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/subscription/portal", h.CreatePortal)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription/portal", `{}`)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("no billing account returns 404", func(t *testing.T) {
		// Subscription exists but has no Stripe customer id.
		store := &mockStore{subResult: aSubscription("FREE")}
		router, h := newOrgRouter(store, &fakeStripe{})
		router.POST("/v1/organizations/:id/subscription/portal", h.CreatePortal)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription/portal", `{}`)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("happy path returns portal url", func(t *testing.T) {
		sub := aSubscription("PRO")
		cust := "cus_1"
		sub.StripeCustomerID = &cust
		store := &mockStore{subResult: sub}
		router, h := newOrgRouter(store, &fakeStripe{portalURL: "https://portal.example/session"})
		router.POST("/v1/organizations/:id/subscription/portal", h.CreatePortal)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription/portal", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "https://portal.example/session")
	})
}

func TestStripeWebhook(t *testing.T) {
	t.Run("billing not configured returns 503", func(t *testing.T) {
		store := &mockStore{}
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("invalid signature returns 400", func(t *testing.T) {
		store := &mockStore{}
		fs := &fakeStripe{parseErr: assertAnError}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("subscription.updated upserts plan from price", func(t *testing.T) {
		store := &mockStore{}
		fs := &fakeStripe{
			proPriceID: "price_pro",
			entPriceID: "price_ent",
			event: stripe.Event{
				Type: "customer.subscription.updated",
				Data: &stripe.EventData{Raw: json.RawMessage(subscriptionEventJSON("price_pro"))},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastUpsertSub)
		assert.Equal(t, "PRO", store.lastUpsertSub.Plan)
		assert.Equal(t, testUserID, store.lastUpsertSub.UserID)
	})

	t.Run("subscription.deleted downgrades to FREE/CANCELED", func(t *testing.T) {
		store := &mockStore{}
		fs := &fakeStripe{
			event: stripe.Event{
				Type: "customer.subscription.deleted",
				Data: &stripe.EventData{Raw: json.RawMessage(subscriptionEventJSON("price_pro"))},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastUpsertSub)
		assert.Equal(t, "FREE", store.lastUpsertSub.Plan)
		assert.Equal(t, "CANCELED", store.lastUpsertSub.Status)
	})

	t.Run("unrecognised price id is ignored without upsert", func(t *testing.T) {
		store := &mockStore{}
		fs := &fakeStripe{
			proPriceID: "price_pro",
			entPriceID: "price_ent",
			event: stripe.Event{
				Type: "customer.subscription.updated",
				Data: &stripe.EventData{Raw: json.RawMessage(subscriptionEventJSON("price_unknown"))},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Nil(t, store.lastUpsertSub)
	})
}

// subscriptionEventJSON builds a Stripe subscription payload carrying the
// user_id metadata and a single line item with the given price ID.
func subscriptionEventJSON(priceID string) string {
	return `{
		"id": "sub_1",
		"status": "active",
		"customer": {"id": "cus_1", "metadata": {"user_id": "test-user-id"}},
		"items": {"data": [{"price": {"id": "` + priceID + `"}}]},
		"current_period_start": 1700000000,
		"current_period_end": 1702592000
	}`
}

var assertAnError = stripeError("boom")

type stripeError string

func (e stripeError) Error() string { return string(e) }
