package handlers_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"upguardly-backend/internal/models"
)

func TestCreateOrg(t *testing.T) {
	// Creating an org is now a paid action: CreateOrg starts a Stripe Checkout
	// session and returns its URL; the org is created later by the webhook.
	t.Run("happy path returns checkout url", func(t *testing.T) {
		store := &mockStore{}
		fs := &fakeStripe{proPriceID: "price_pro", customerID: "cus_1", checkoutURL: "https://checkout.example/session"}
		router, h := newOrgRouter(store, fs)
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"Acme","plan":"PRO"}`)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "https://checkout.example/session")
		assert.Equal(t, "Acme", fs.lastCheckoutMeta["org_name"])
		assert.Equal(t, "PRO", fs.lastCheckoutMeta["plan"])
		assert.Equal(t, testUserID, fs.lastCheckoutMeta["user_id"])
	})

	t.Run("billing not configured returns 503", func(t *testing.T) {
		store := &mockStore{}
		router, h := newOrgRouter(store, nil) // nil stripe
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"Acme","plan":"PRO"}`)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("user already in an org returns 409", func(t *testing.T) {
		store := &mockStore{orgsResult: []models.Organization{{ID: "org-1", Name: "Existing"}}}
		router, h := newOrgRouter(store, &fakeStripe{proPriceID: "price_pro"})
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"Another","plan":"PRO"}`)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("missing plan returns 400", func(t *testing.T) {
		store := &mockStore{}
		router, h := newOrgRouter(store, &fakeStripe{proPriceID: "price_pro"})
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"Acme"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("name too short returns 400", func(t *testing.T) {
		store := &mockStore{}
		router, h := newOrgRouter(store, &fakeStripe{proPriceID: "price_pro"})
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"A","plan":"PRO"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestAcceptInvitation(t *testing.T) {
	pendingInvite := func() *models.Invitation {
		return &models.Invitation{
			ID:        "inv-1",
			OrgID:     "org-1",
			Email:     "user@example.com",
			Role:      models.OrgRoleMember,
			Status:    "PENDING",
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}
	}

	t.Run("user already in an org returns 409", func(t *testing.T) {
		store := &mockStore{
			inviteResult: pendingInvite(),
			orgsResult:   []models.Organization{{ID: "org-2", Name: "Existing"}},
		}
		router, h := newTestRouter(store)
		router.POST("/v1/invitations/:token/accept", h.AcceptInvitation)

		w := doRequest(router, "POST", "/v1/invitations/sometoken/accept", "")

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("conflict from store returns 409", func(t *testing.T) {
		// Pre-check passes (no existing orgs) but the store reports a unique
		// violation, e.g. a concurrent membership insert.
		store := &mockStore{
			inviteResult:    pendingInvite(),
			acceptInviteErr: models.ErrConflict,
		}
		router, h := newTestRouter(store)
		router.POST("/v1/invitations/:token/accept", h.AcceptInvitation)

		w := doRequest(router, "POST", "/v1/invitations/sometoken/accept", "")

		assert.Equal(t, http.StatusConflict, w.Code)
	})
}

func TestDeleteOrg(t *testing.T) {
	t.Run("active subscription blocks deletion with 409", func(t *testing.T) {
		store := &mockStore{subResult: aSubscription("PRO")} // Status ACTIVE
		router, h := newOrgRouter(store, nil)
		router.DELETE("/v1/organizations/:id", h.DeleteOrg)

		w := doRequest(router, "DELETE", "/v1/organizations/test-org-id", "")

		assert.Equal(t, http.StatusConflict, w.Code)
		assert.False(t, store.deleteOrgCalled)
	})

	t.Run("canceled subscription allows deletion with 204", func(t *testing.T) {
		sub := aSubscription("FREE")
		sub.Status = "CANCELED"
		store := &mockStore{subResult: sub}
		router, h := newOrgRouter(store, nil)
		router.DELETE("/v1/organizations/:id", h.DeleteOrg)

		w := doRequest(router, "DELETE", "/v1/organizations/test-org-id", "")

		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.True(t, store.deleteOrgCalled)
	})

	t.Run("no subscription allows deletion with 204", func(t *testing.T) {
		store := &mockStore{} // GetSubscription → ErrNotFound
		router, h := newOrgRouter(store, nil)
		router.DELETE("/v1/organizations/:id", h.DeleteOrg)

		w := doRequest(router, "DELETE", "/v1/organizations/test-org-id", "")

		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.True(t, store.deleteOrgCalled)
	})

	t.Run("missing orgId returns 400", func(t *testing.T) {
		store := &mockStore{}
		router, h := newTestRouter(store) // no orgId in context
		router.DELETE("/v1/organizations", h.DeleteOrg)

		w := doRequest(router, "DELETE", "/v1/organizations", "")

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("store error returns 500", func(t *testing.T) {
		store := &mockStore{deleteOrgErr: models.ErrNotFound}
		router, h := newOrgRouter(store, nil)
		router.DELETE("/v1/organizations/:id", h.DeleteOrg)

		w := doRequest(router, "DELETE", "/v1/organizations/test-org-id", "")

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}
