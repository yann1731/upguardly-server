package handlers_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"upguardly-backend/internal/models"
)

func TestCreateOrg(t *testing.T) {
	// Org creation is an ENTERPRISE-only feature, created synchronously.
	t.Run("enterprise account creates org (201)", func(t *testing.T) {
		store := &mockStore{
			subResult: aSubscription("ENTERPRISE"),
			orgResult: &models.Organization{ID: "org-1", Name: "Acme"},
		}
		router, h := newTestRouter(store)
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"Acme"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("non-enterprise account is rejected (402)", func(t *testing.T) {
		store := &mockStore{subResult: aSubscription("PRO")}
		router, h := newTestRouter(store)
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"Acme"}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
	})

	t.Run("free account (no subscription) is rejected (402)", func(t *testing.T) {
		store := &mockStore{} // GetSubscriptionByUser → ErrNotFound → FREE
		router, h := newTestRouter(store)
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"Acme"}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
	})

	t.Run("canceled enterprise subscription is rejected (402)", func(t *testing.T) {
		// A CANCELED status carries no entitlement even though the stored
		// plan name is still ENTERPRISE (e.g. the subscription went unpaid).
		sub := aSubscription("ENTERPRISE")
		sub.Status = "CANCELED"
		store := &mockStore{subResult: sub}
		router, h := newTestRouter(store)
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"Acme"}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
	})

	t.Run("user already in an org returns 409", func(t *testing.T) {
		store := &mockStore{
			subResult:  aSubscription("ENTERPRISE"),
			orgsResult: []models.Organization{{ID: "org-1", Name: "Existing"}},
		}
		router, h := newTestRouter(store)
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"Another"}`)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("duplicate name returns 409", func(t *testing.T) {
		store := &mockStore{subResult: aSubscription("ENTERPRISE"), createOrgErr: models.ErrConflict}
		router, h := newTestRouter(store)
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"Taken"}`)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("name too short returns 400", func(t *testing.T) {
		store := &mockStore{subResult: aSubscription("ENTERPRISE")}
		router, h := newTestRouter(store)
		router.POST("/v1/organizations", h.CreateOrg)

		w := doRequest(router, "POST", "/v1/organizations", `{"name":"A"}`)

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
	// The subscription belongs to the owner's account, not the org, so deletion
	// is no longer gated on subscription status.
	t.Run("deletes org with 204", func(t *testing.T) {
		store := &mockStore{}
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
