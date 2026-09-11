package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v85"

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

	t.Run("reconcile is TTL-cached: repeat reads skip Stripe", func(t *testing.T) {
		sub := aSubscription("PRO")
		cust := "cus_1"
		sub.StripeCustomerID = &cust
		store := &mockStore{subResult: sub}
		fs := &fakeStripe{proPriceID: "price_pro", activeSub: aStripeSub("price_pro", false)}
		router, h := newOrgRouter(store, fs)
		router.GET("/v1/organizations/:id/subscription", h.GetSubscription)

		for i := 0; i < 5; i++ {
			w := doRequest(router, "GET", "/v1/organizations/test-org-id/subscription", "")
			assert.Equal(t, http.StatusOK, w.Code)
		}

		assert.Equal(t, 1, fs.getActiveSubCalls, "only the first read within the TTL should hit Stripe")
	})

	t.Run("cancel invalidates the reconcile cache", func(t *testing.T) {
		sub := aSubscription("PRO")
		cust := "cus_1"
		subID := "sub_1"
		sub.StripeCustomerID = &cust
		sub.StripeSubscriptionID = &subID
		store := &mockStore{subResult: sub}
		fs := &fakeStripe{proPriceID: "price_pro", activeSub: aStripeSub("price_pro", true)}
		router, h := newOrgRouter(store, fs)
		router.GET("/v1/organizations/:id/subscription", h.GetSubscription)
		router.DELETE("/v1/organizations/:id/subscription", h.CancelSubscription)

		doRequest(router, "GET", "/v1/organizations/test-org-id/subscription", "")
		require.Equal(t, 1, fs.getActiveSubCalls)

		// Within the TTL a read would normally skip Stripe; cancel clears the
		// entry so the next read sees live state immediately.
		doRequest(router, "DELETE", "/v1/organizations/test-org-id/subscription", "")
		w := doRequest(router, "GET", "/v1/organizations/test-org-id/subscription", "")

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, 2, fs.getActiveSubCalls, "read after cancel must reconcile again")
		assert.Contains(t, w.Body.String(), `"cancelAtPeriodEnd":true`)
	})

	t.Run("a scheduled cancellation survives reads served from the record", func(t *testing.T) {
		// The regression this guards: cancelAtPeriodEnd used to be stitched onto
		// the response only on the reconcile path, so every read inside
		// reconcileTTL reported false on a plan whose status is legitimately
		// still ACTIVE — and the billing page announced a renewal that was not
		// coming. Only the first of these reads reconciles; all of them must
		// agree.
		sub := aSubscription("PRO")
		cust := "cus_1"
		sub.StripeCustomerID = &cust
		store := &mockStore{subResult: sub}
		fs := &fakeStripe{proPriceID: "price_pro", activeSub: aStripeSub("price_pro", true)}
		router, h := newOrgRouter(store, fs)
		router.GET("/v1/organizations/:id/subscription", h.GetSubscription)

		for i := 0; i < 3; i++ {
			w := doRequest(router, "GET", "/v1/organizations/test-org-id/subscription", "")
			require.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Body.String(), `"cancelAtPeriodEnd":true`, "read %d", i+1)
			assert.Contains(t, w.Body.String(), `"status":"ACTIVE"`, "read %d: grace is preserved", i+1)
		}
		require.Equal(t, 1, fs.getActiveSubCalls, "only the first read should hit Stripe")
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

	t.Run("persists the flag instead of only reporting it", func(t *testing.T) {
		// Stripe is the source of truth, but the record has to carry the flag
		// too: reads inside reconcileTTL never ask Stripe.
		sub := aSubscription("PRO")
		subID := "sub_1"
		sub.StripeSubscriptionID = &subID
		store := &mockStore{subResult: sub}
		router, h := newOrgRouter(store, &fakeStripe{})
		router.DELETE("/v1/organizations/:id/subscription", h.CancelSubscription)

		doRequest(router, "DELETE", "/v1/organizations/test-org-id/subscription", "")

		require.NotNil(t, store.lastUpsertSub)
		assert.True(t, store.lastUpsertSub.CancelAtPeriodEnd)
		// The plan keeps running until the period ends, so nothing else moves.
		assert.Equal(t, "PRO", store.lastUpsertSub.Plan)
		assert.Equal(t, "ACTIVE", store.lastUpsertSub.Status)
		assert.Nil(t, store.lastReconcile, "entitlement is unchanged, so monitors must not be snapped")
	})
}

func TestResumeSubscription(t *testing.T) {
	t.Run("billing not configured returns 503", func(t *testing.T) {
		store := &mockStore{}
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/subscription/resume", h.ResumeSubscription)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription/resume", "")

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("no billing subscription returns 404", func(t *testing.T) {
		store := &mockStore{subResult: aSubscription("PRO")} // no StripeSubscriptionID
		router, h := newOrgRouter(store, &fakeStripe{})
		router.POST("/v1/organizations/:id/subscription/resume", h.ResumeSubscription)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription/resume", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("an already-ended subscription returns 409", func(t *testing.T) {
		// The period lapsed, so there is no live subscription to un-schedule —
		// the user has to check out again.
		sub := aSubscription("PRO")
		subID := "sub_1"
		sub.StripeSubscriptionID = &subID
		sub.Status = "CANCELED"
		store := &mockStore{subResult: sub}
		fs := &fakeStripe{}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/organizations/:id/subscription/resume", h.ResumeSubscription)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription/resume", "")

		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Nil(t, fs.lastCancelAtPeriodEnd, "Stripe must not be called")
	})

	t.Run("clears the scheduled cancellation", func(t *testing.T) {
		sub := aSubscription("PRO")
		subID := "sub_1"
		sub.StripeSubscriptionID = &subID
		sub.CancelAtPeriodEnd = true
		store := &mockStore{subResult: sub}
		fs := &fakeStripe{}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/organizations/:id/subscription/resume", h.ResumeSubscription)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription/resume", "")

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, fs.lastCancelAtPeriodEnd)
		assert.False(t, *fs.lastCancelAtPeriodEnd)
		require.NotNil(t, store.lastUpsertSub)
		assert.False(t, store.lastUpsertSub.CancelAtPeriodEnd)
		assert.Contains(t, w.Body.String(), `"cancelAtPeriodEnd":false`)
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
		Items: &stripe.SubscriptionItemList{
			Data: []*stripe.SubscriptionItem{{
				Price:              &stripe.Price{ID: priceID},
				CurrentPeriodStart: 1700000000,
				CurrentPeriodEnd:   1702592000,
			}},
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

	t.Run("stored customer id skips the Stripe customer lookup", func(t *testing.T) {
		sub := aSubscription("FREE")
		cust := "cus_stored"
		sub.StripeCustomerID = &cust
		store := &mockStore{subResult: sub}
		fs := &fakeStripe{proPriceID: "price_pro", checkoutURL: "https://checkout.example/session"}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/organizations/:id/subscription", h.CreateCheckout)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription", `{"plan":"PRO"}`)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.False(t, fs.ensureCalled, "EnsureCustomer must not be called when a customer ID is already stored")
	})

	t.Run("newly resolved customer id is persisted", func(t *testing.T) {
		store := &mockStore{} // no subscription record yet
		fs := &fakeStripe{proPriceID: "price_pro", customerID: "cus_new", checkoutURL: "https://checkout.example/session"}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/organizations/:id/subscription", h.CreateCheckout)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription", `{"plan":"PRO"}`)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.True(t, fs.ensureCalled)
		require.NotNil(t, store.lastUpsertSub)
		require.NotNil(t, store.lastUpsertSub.StripeCustomerID)
		assert.Equal(t, "cus_new", *store.lastUpsertSub.StripeCustomerID)
		// Persisting the customer ID must not grant a plan.
		assert.Equal(t, "FREE", store.lastUpsertSub.Plan)
	})

	t.Run("invited org member can buy a personal plan", func(t *testing.T) {
		// The subscription covers their personal workspace; the org workspace
		// stays on the owner's plan.
		store := &mockStore{
			orgsResult:       []models.Organization{{ID: "test-org-id", Name: "Acme", OwnerID: "owner-id"}},
			membershipResult: aMembership(),
		}
		fs := &fakeStripe{proPriceID: "price_pro", customerID: "cus_1", checkoutURL: "https://checkout.example/session"}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/organizations/:id/subscription", h.CreateCheckout)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription", `{"plan":"PRO"}`)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.True(t, fs.ensureCalled)
	})

	t.Run("org owner can still check out", func(t *testing.T) {
		owner := aMembership()
		owner.Role = models.OrgRoleOwner
		store := &mockStore{
			orgsResult:       []models.Organization{{ID: "test-org-id", Name: "Acme", OwnerID: testUserID}},
			membershipResult: owner,
		}
		fs := &fakeStripe{proPriceID: "price_pro", customerID: "cus_1", checkoutURL: "https://checkout.example/session"}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/organizations/:id/subscription", h.CreateCheckout)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/subscription", `{"plan":"PRO"}`)

		assert.Equal(t, http.StatusOK, w.Code)
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

	t.Run("subscription.updated records a scheduled cancellation", func(t *testing.T) {
		// Cancelling — in-app or from the Stripe portal — arrives as an updated
		// event with status still "active" and cancel_at_period_end true. Status
		// alone cannot express it, so dropping the flag here left the record
		// indistinguishable from a renewing plan.
		store := &mockStore{}
		fs := &fakeStripe{
			proPriceID: "price_pro",
			event: stripe.Event{
				Type: "customer.subscription.updated",
				Data: &stripe.EventData{Raw: json.RawMessage(subscriptionEventJSONCanceling("price_pro"))},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastUpsertSub)
		assert.True(t, store.lastUpsertSub.CancelAtPeriodEnd)
		assert.Equal(t, "ACTIVE", store.lastUpsertSub.Status, "grace runs to the period end")
		assert.Equal(t, "PRO", store.lastUpsertSub.Plan)
	})

	t.Run("unpaid status is stored as CANCELED, never ACTIVE", func(t *testing.T) {
		// "unpaid" means payment retries are exhausted; Stripe never emits a
		// deleted event for it, so this webhook is the only signal. Mapping
		// it (or any unknown status) to ACTIVE would grant a free ride.
		store := &mockStore{}
		fs := &fakeStripe{
			proPriceID: "price_pro",
			event: stripe.Event{
				Type: "customer.subscription.updated",
				Data: &stripe.EventData{Raw: json.RawMessage(subscriptionEventJSONWithStatus("price_pro", "unpaid"))},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastUpsertSub)
		assert.Equal(t, "CANCELED", store.lastUpsertSub.Status)
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

	t.Run("subscription.deleted clears the scheduled cancellation", func(t *testing.T) {
		// Once the period has actually ended the cancellation is done, not
		// pending — leaving the flag set would keep the billing page announcing
		// a future cancel date on a FREE plan.
		sub := aSubscription("PRO")
		sub.CancelAtPeriodEnd = true
		store := &mockStore{subResult: sub}
		fs := &fakeStripe{
			event: stripe.Event{
				Type: "customer.subscription.deleted",
				Data: &stripe.EventData{Raw: json.RawMessage(subscriptionEventJSON("price_pro"))},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		require.NotNil(t, store.lastUpsertSub)
		assert.False(t, store.lastUpsertSub.CancelAtPeriodEnd)
	})

	t.Run("subscription.deleted reconciles monitors to FREE", func(t *testing.T) {
		// The deleted webhook is where a scheduled cancellation actually
		// lands (Stripe fires it when the paid period ends) — this is the
		// moment existing monitors must be snapped to FREE limits.
		store := &mockStore{subResult: aSubscription("PRO")}
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
		require.NotNil(t, store.lastReconcile)
		assert.Equal(t, testUserID, store.lastReconcile.UserID)
		assert.Equal(t, "PRO", store.lastReconcile.OldPlan)
		assert.Equal(t, "FREE", store.lastReconcile.NewPlan)
	})

	t.Run("subscription.updated upgrade reconciles monitors to the paid plan", func(t *testing.T) {
		store := &mockStore{} // no record yet → effective FREE
		fs := &fakeStripe{
			proPriceID: "price_pro",
			event: stripe.Event{
				Type: "customer.subscription.updated",
				Data: &stripe.EventData{Raw: json.RawMessage(subscriptionEventJSON("price_pro"))},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastReconcile)
		assert.Equal(t, "FREE", store.lastReconcile.OldPlan)
		assert.Equal(t, "PRO", store.lastReconcile.NewPlan)
	})

	t.Run("scheduling a cancel does not reconcile (grace until period end)", func(t *testing.T) {
		// Cancelling at period end fires subscription.updated with the plan
		// still active — the effective plan is unchanged until the deleted
		// event lands, so monitors must keep their paid intervals.
		store := &mockStore{subResult: aSubscription("PRO")}
		fs := &fakeStripe{
			proPriceID: "price_pro",
			event: stripe.Event{
				Type: "customer.subscription.updated",
				Data: &stripe.EventData{Raw: json.RawMessage(subscriptionEventJSON("price_pro"))},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Nil(t, store.lastReconcile, "same effective plan must not touch monitors")
	})

	t.Run("unpaid on a paid record reconciles monitors to FREE", func(t *testing.T) {
		// "unpaid" maps to CANCELED and Stripe never emits a deleted event
		// for it, so this webhook is also the enforcement point.
		store := &mockStore{subResult: aSubscription("PRO")}
		fs := &fakeStripe{
			proPriceID: "price_pro",
			event: stripe.Event{
				Type: "customer.subscription.updated",
				Data: &stripe.EventData{Raw: json.RawMessage(subscriptionEventJSONWithStatus("price_pro", "unpaid"))},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastReconcile)
		assert.Equal(t, "PRO", store.lastReconcile.OldPlan)
		assert.Equal(t, "FREE", store.lastReconcile.NewPlan)
	})

	t.Run("payment failure keeps the plan and does not reconcile", func(t *testing.T) {
		// PAST_DUE is a grace status: entitlement (and monitors) unchanged
		// while Stripe retries the payment.
		store := &mockStore{subResult: aSubscription("PRO")}
		fs := &fakeStripe{
			event: stripe.Event{
				Type: "invoice.payment_failed",
				Data: &stripe.EventData{Raw: json.RawMessage(`{
					"customer": {"id": "cus_1", "metadata": {"user_id": "test-user-id"}},
					"parent": {
						"type": "subscription_details",
						"subscription_details": {"subscription": {"id": "sub_1"}}
					}
				}`)},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastUpsertSub)
		assert.Equal(t, "PRO", store.lastUpsertSub.Plan)
		assert.Equal(t, "PAST_DUE", store.lastUpsertSub.Status)
		assert.Nil(t, store.lastReconcile)
	})

	t.Run("payment failure on a non-subscription invoice is ignored", func(t *testing.T) {
		// A one-off invoice has no parent.subscription_details, so there is no
		// entitlement to downgrade. This also pins the field location: the
		// pre-Basil top-level "subscription" must not be read, or every
		// subscription invoice silently looks like this one.
		store := &mockStore{subResult: aSubscription("PRO")}
		fs := &fakeStripe{
			event: stripe.Event{
				Type: "invoice.payment_failed",
				Data: &stripe.EventData{Raw: json.RawMessage(`{
					"customer": {"id": "cus_1", "metadata": {"user_id": "test-user-id"}},
					"subscription": {"id": "sub_1"},
					"parent": null
				}`)},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Nil(t, store.lastUpsertSub)
	})

	t.Run("pause suspends entitlement and resume restores it", func(t *testing.T) {
		// paused has no member in the SubscriptionStatus enum, so it collapses
		// to CANCELED (no entitlement). Resume is announced only by
		// customer.subscription.resumed — without that case the user would
		// stay downgraded.
		store := &mockStore{subResult: aSubscription("PRO")}
		fs := &fakeStripe{
			proPriceID: "price_pro",
			event: stripe.Event{
				Type: "customer.subscription.paused",
				Data: &stripe.EventData{Raw: json.RawMessage(subscriptionEventJSONWithStatus("price_pro", "paused"))},
			},
		}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/webhooks/stripe", h.StripeWebhook)

		w := doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastUpsertSub)
		assert.Equal(t, "CANCELED", store.lastUpsertSub.Status)

		store.lastUpsertSub = nil
		fs.event = stripe.Event{
			Type: "customer.subscription.resumed",
			Data: &stripe.EventData{Raw: json.RawMessage(subscriptionEventJSONWithStatus("price_pro", "active"))},
		}

		w = doRequest(router, "POST", "/v1/webhooks/stripe", `{}`)

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastUpsertSub)
		assert.Equal(t, "ACTIVE", store.lastUpsertSub.Status)
		assert.Equal(t, "PRO", store.lastUpsertSub.Plan)
	})

	t.Run("stale-record reconcile against Stripe also reconciles monitors", func(t *testing.T) {
		// GetSubscription healing a missed deleted-webhook (paid record, no
		// live Stripe subscription) is the third enforcement point.
		sub := aSubscription("PRO")
		cust := "cus_1"
		sub.StripeCustomerID = &cust
		store := &mockStore{subResult: sub}
		fs := &fakeStripe{activeSub: nil} // Stripe has no live subscription
		router, h := newOrgRouter(store, fs)
		router.GET("/v1/organizations/:id/subscription", h.GetSubscription)

		w := doRequest(router, "GET", "/v1/organizations/test-org-id/subscription", "")

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastReconcile)
		assert.Equal(t, "PRO", store.lastReconcile.OldPlan)
		assert.Equal(t, "FREE", store.lastReconcile.NewPlan)
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
	return subscriptionEventJSONWithStatus(priceID, "active")
}

// subscriptionEventJSONCanceling is what Stripe sends when a cancellation is
// scheduled: the subscription is still active, with cancel_at_period_end set.
func subscriptionEventJSONCanceling(priceID string) string {
	return `{
		"id": "sub_1",
		"status": "active",
		"cancel_at_period_end": true,
		"customer": {"id": "cus_1", "metadata": {"user_id": "test-user-id"}},
		"items": {"data": [{
			"price": {"id": "` + priceID + `"},
			"current_period_start": 1700000000,
			"current_period_end": 1702592000
		}]}
	}`
}

func subscriptionEventJSONWithStatus(priceID, status string) string {
	return `{
		"id": "sub_1",
		"status": "` + status + `",
		"customer": {"id": "cus_1", "metadata": {"user_id": "test-user-id"}},
		"items": {"data": [{
			"price": {"id": "` + priceID + `"},
			"current_period_start": 1700000000,
			"current_period_end": 1702592000
		}]}
	}`
}

var assertAnError = stripeError("boom")

type stripeError string

func (e stripeError) Error() string { return string(e) }
