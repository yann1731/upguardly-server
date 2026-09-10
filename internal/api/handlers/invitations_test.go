package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"upguardly-backend/internal/models"
)

func TestListMembersIncludesEmail(t *testing.T) {
	store := &mockStore{
		membersResult: []models.OrganizationMember{
			{ID: "m-1", OrgID: testOrgID, UserID: "owner-1", Role: models.OrgRoleOwner},
			{ID: "m-2", OrgID: testOrgID, UserID: "ghost", Role: models.OrgRoleMember},
		},
	}
	router, h := newOrgRouter(store, nil)
	h.UserEmailLookup = func(userID string) (string, error) {
		if userID == "owner-1" {
			return "owner@example.com", nil
		}
		return "", errors.New("user not found")
	}
	router.GET("/v1/organizations/:id/members", h.ListMembers)

	w := doRequest(router, "GET", "/v1/organizations/test-org-id/members", "")

	require.Equal(t, http.StatusOK, w.Code)
	var got []models.OrganizationMember
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got, 2)
	assert.Equal(t, "owner@example.com", got[0].Email)
	// A failed lookup degrades to a blank email instead of failing the list.
	assert.Equal(t, "", got[1].Email)
}

func TestAcceptInvitationEmailMatch(t *testing.T) {
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
	acceptingStore := func() *mockStore {
		return &mockStore{
			inviteResult:     pendingInvite(),
			orgResult:        &models.Organization{ID: "org-1", Name: "Acme", OwnerID: "owner-1"},
			subResult:        aSubscription("ENTERPRISE"),
			membershipResult: aMembership(),
		}
	}

	t.Run("matching email in a different case is accepted", func(t *testing.T) {
		router, h := newTestRouter(acceptingStore())
		h.UserEmailLookup = func(string) (string, error) { return "User@Example.com", nil }
		router.POST("/v1/invitations/:token/accept", h.AcceptInvitation)

		w := doRequest(router, "POST", "/v1/invitations/sometoken/accept", "")

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("different account email is rejected with email_mismatch", func(t *testing.T) {
		store := acceptingStore()
		router, h := newTestRouter(store)
		h.UserEmailLookup = func(string) (string, error) { return "someone-else@example.com", nil }
		router.POST("/v1/invitations/:token/accept", h.AcceptInvitation)

		w := doRequest(router, "POST", "/v1/invitations/sometoken/accept", "")

		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), `"code":"email_mismatch"`)
		assert.Contains(t, w.Body.String(), `"invitedEmail":"user@example.com"`)
		assert.Zero(t, store.lastAcceptMaxSeats, "store accept must not run on a mismatch")
	})

	t.Run("email lookup failure returns 500", func(t *testing.T) {
		router, h := newTestRouter(acceptingStore())
		h.UserEmailLookup = func(string) (string, error) { return "", errors.New("supertokens down") }
		router.POST("/v1/invitations/:token/accept", h.AcceptInvitation)

		w := doRequest(router, "POST", "/v1/invitations/sometoken/accept", "")

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("invitation invalidated inside the transaction returns 409", func(t *testing.T) {
		// Revoked between the handler's pre-check and the store transaction:
		// the store's locked re-read finds no pending row.
		store := acceptingStore()
		store.acceptInviteErr = models.ErrNotFound
		router, h := newTestRouter(store)
		h.UserEmailLookup = func(string) (string, error) { return "user@example.com", nil }
		router.POST("/v1/invitations/:token/accept", h.AcceptInvitation)

		w := doRequest(router, "POST", "/v1/invitations/sometoken/accept", "")

		assert.Equal(t, http.StatusConflict, w.Code)
	})
}

func TestGetInvitationPreview(t *testing.T) {
	t.Run("unknown token returns 404", func(t *testing.T) {
		router, h := newTestRouter(&mockStore{})
		router.GET("/v1/invitations/:token", h.GetInvitationPreview)

		w := doRequest(router, "GET", "/v1/invitations/nope", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("valid token returns org, email, role and status", func(t *testing.T) {
		store := &mockStore{
			inviteResult: &models.Invitation{
				ID: "inv-1", OrgID: "org-1", Email: "user@example.com", Role: models.OrgRoleAdmin,
				Status: "PENDING", Token: "hash-must-not-leak", ExpiresAt: time.Now().Add(time.Hour),
			},
			orgResult: &models.Organization{ID: "org-1", Name: "Acme"},
		}
		router, h := newTestRouter(store)
		router.GET("/v1/invitations/:token", h.GetInvitationPreview)

		w := doRequest(router, "GET", "/v1/invitations/sometoken", "")

		require.Equal(t, http.StatusOK, w.Code)
		var got models.InvitationPreview
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Equal(t, "Acme", got.OrgName)
		assert.Equal(t, "user@example.com", got.Email)
		assert.Equal(t, models.OrgRoleAdmin, got.Role)
		assert.Equal(t, "PENDING", got.Status)
		assert.NotContains(t, w.Body.String(), "hash-must-not-leak")
		assert.NotContains(t, w.Body.String(), "inv-1")
	})

	t.Run("lapsed pending invitation is reported as EXPIRED", func(t *testing.T) {
		store := &mockStore{
			inviteResult: &models.Invitation{
				ID: "inv-1", OrgID: "org-1", Email: "user@example.com", Role: models.OrgRoleMember,
				Status: "PENDING", ExpiresAt: time.Now().Add(-time.Hour),
			},
			orgResult: &models.Organization{ID: "org-1", Name: "Acme"},
		}
		router, h := newTestRouter(store)
		router.GET("/v1/invitations/:token", h.GetInvitationPreview)

		w := doRequest(router, "GET", "/v1/invitations/sometoken", "")

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), `"status":"EXPIRED"`)
	})
}

func TestCreateInvitationDuplicates(t *testing.T) {
	t.Run("live pending invitation blocks a re-invite, case-insensitively", func(t *testing.T) {
		store := enterpriseOrgStore()
		store.invitationsResult = []models.Invitation{
			{ID: "inv-old", Email: "new@example.com", Status: "PENDING", ExpiresAt: time.Now().Add(time.Hour)},
		}
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/invitations", h.CreateInvitation)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/invitations",
			`{"email":"NEW@Example.com","role":"MEMBER"}`)

		assert.Equal(t, http.StatusConflict, w.Code)
		assert.Empty(t, store.expiredInvIDs)
	})

	t.Run("expired pending invitation is superseded (resend)", func(t *testing.T) {
		store := enterpriseOrgStore()
		store.invitationsResult = []models.Invitation{
			{ID: "inv-old", Email: "new@example.com", Status: "PENDING", ExpiresAt: time.Now().Add(-time.Hour)},
		}
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/invitations", h.CreateInvitation)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/invitations",
			`{"email":"new@example.com","role":"MEMBER"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
		assert.Equal(t, []string{"inv-old"}, store.expiredInvIDs)
	})

	t.Run("invited email is stored normalized", func(t *testing.T) {
		store := enterpriseOrgStore()
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/invitations", h.CreateInvitation)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/invitations",
			`{"email":"New@Example.com","role":"MEMBER"}`)

		require.Equal(t, http.StatusCreated, w.Code)
		assert.Contains(t, w.Body.String(), `"email":"new@example.com"`)
	})
}
