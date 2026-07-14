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

func TestCreateInvitationSeats(t *testing.T) {
	// Login seats: the owner is free; each non-owner member and each pending
	// non-expired invitation holds one of the plan's 3 seats.
	t.Run("invite under the cap succeeds (201)", func(t *testing.T) {
		store := enterpriseOrgStore()
		store.memberCount = 1
		store.pendingInvCount = 1
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/invitations", h.CreateInvitation)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/invitations",
			`{"email":"new@example.com","role":"MEMBER"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("members plus pending invitations at the cap returns 402", func(t *testing.T) {
		store := enterpriseOrgStore()
		store.memberCount = 2
		store.pendingInvCount = 1
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/invitations", h.CreateInvitation)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/invitations",
			`{"email":"new@example.com","role":"MEMBER"}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
	})

	t.Run("lapsed plan blocks any invite (402)", func(t *testing.T) {
		// No subscription row → FREE → MaxLoginSeats 0.
		store := &mockStore{orgResult: &models.Organization{ID: testOrgID, Name: "Acme", OwnerID: testUserID}}
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/invitations", h.CreateInvitation)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/invitations",
			`{"email":"new@example.com","role":"MEMBER"}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
	})
}

func TestGetOrgSeats(t *testing.T) {
	t.Run("returns seat usage alongside the org", func(t *testing.T) {
		store := enterpriseOrgStore()
		store.memberCount = 1
		store.pendingInvCount = 1
		store.recipientCount = 2
		router, h := newOrgRouter(store, nil)
		router.GET("/v1/organizations/:id", h.GetOrg)

		w := doRequest(router, "GET", "/v1/organizations/test-org-id", "")

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"loginSeatsUsed":2`)
		assert.Contains(t, w.Body.String(), `"maxLoginSeats":3`)
		assert.Contains(t, w.Body.String(), `"alertRecipientsUsed":2`)
		assert.Contains(t, w.Body.String(), `"maxAlertRecipients":3`)
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

	t.Run("seat limit from store returns 409", func(t *testing.T) {
		// The transactional re-check inside AcceptInvitation lost the race:
		// the org filled up between the invite and the accept.
		store := &mockStore{
			inviteResult:    pendingInvite(),
			acceptInviteErr: models.ErrSeatLimit,
		}
		router, h := newTestRouter(store)
		router.POST("/v1/invitations/:token/accept", h.AcceptInvitation)

		w := doRequest(router, "POST", "/v1/invitations/sometoken/accept", "")

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("passes the org plan's seat cap to the store", func(t *testing.T) {
		store := &mockStore{
			inviteResult:     pendingInvite(),
			orgResult:        &models.Organization{ID: "org-1", Name: "Acme", OwnerID: "owner-1"},
			subResult:        aSubscription("ENTERPRISE"),
			membershipResult: aMembership(),
		}
		router, h := newTestRouter(store)
		router.POST("/v1/invitations/:token/accept", h.AcceptInvitation)

		w := doRequest(router, "POST", "/v1/invitations/sometoken/accept", "")

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, 3, store.lastAcceptMaxSeats)
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
