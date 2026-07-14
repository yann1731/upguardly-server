package handlers_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"upguardly-backend/internal/models"
)

// enterpriseOrgStore returns a mockStore where the org resolves to an
// ENTERPRISE owner, the baseline for alerting-seat tests.
func enterpriseOrgStore() *mockStore {
	return &mockStore{
		orgResult: &models.Organization{ID: testOrgID, Name: "Acme", OwnerID: testUserID},
		subResult: aSubscription("ENTERPRISE"),
	}
}

func TestCreateOrgAlertRecipient(t *testing.T) {
	t.Run("creates recipient under the cap (201)", func(t *testing.T) {
		store := enterpriseOrgStore()
		store.recipientCount = 2
		store.recipientResult = &models.OrgAlertRecipient{ID: "rec-1", OrgID: testOrgID, Channel: models.AlertChannelEMAIL, Target: "oncall@example.com"}
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/alert-recipients", h.CreateOrgAlertRecipient)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/alert-recipients",
			`{"channel":"EMAIL","target":"oncall@example.com"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
		assert.Equal(t, "EMAIL", store.lastRecipientChannel)
		assert.Equal(t, "oncall@example.com", store.lastRecipientTarget)
	})

	t.Run("seat cap reached returns 402", func(t *testing.T) {
		store := enterpriseOrgStore()
		store.recipientCount = 3
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/alert-recipients", h.CreateOrgAlertRecipient)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/alert-recipients",
			`{"channel":"EMAIL","target":"oncall@example.com"}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
	})

	t.Run("non-enterprise org returns 402", func(t *testing.T) {
		// Org owner's plan lapsed to FREE (no subscription row): recipients
		// are not included at all (MaxAlertRecipients 0).
		store := &mockStore{orgResult: &models.Organization{ID: testOrgID, Name: "Acme", OwnerID: testUserID}}
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/alert-recipients", h.CreateOrgAlertRecipient)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/alert-recipients",
			`{"channel":"EMAIL","target":"oncall@example.com"}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
	})

	t.Run("invalid email returns 400", func(t *testing.T) {
		store := enterpriseOrgStore()
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/alert-recipients", h.CreateOrgAlertRecipient)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/alert-recipients",
			`{"channel":"EMAIL","target":"not-an-email"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("invalid phone number returns 400", func(t *testing.T) {
		store := enterpriseOrgStore()
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/alert-recipients", h.CreateOrgAlertRecipient)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/alert-recipients",
			`{"channel":"SMS","target":"555-1234"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("disallowed channel returns 400", func(t *testing.T) {
		// Recipients are notify-only contacts: only EMAIL and SMS. The binding
		// oneof rejects everything else.
		store := enterpriseOrgStore()
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/alert-recipients", h.CreateOrgAlertRecipient)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/alert-recipients",
			`{"channel":"TELEGRAM","target":"123456789"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("duplicate recipient returns 409", func(t *testing.T) {
		store := enterpriseOrgStore()
		store.recipientErr = models.ErrConflict
		router, h := newOrgRouter(store, nil)
		router.POST("/v1/organizations/:id/alert-recipients", h.CreateOrgAlertRecipient)

		w := doRequest(router, "POST", "/v1/organizations/test-org-id/alert-recipients",
			`{"channel":"EMAIL","target":"oncall@example.com"}`)

		assert.Equal(t, http.StatusConflict, w.Code)
	})
}

func TestListOrgAlertRecipients(t *testing.T) {
	t.Run("lists recipients (200)", func(t *testing.T) {
		store := enterpriseOrgStore()
		store.recipientsResult = []models.OrgAlertRecipient{
			{ID: "rec-1", OrgID: testOrgID, Channel: models.AlertChannelEMAIL, Target: "oncall@example.com"},
			{ID: "rec-2", OrgID: testOrgID, Channel: models.AlertChannelSMS, Target: "+12125551234"},
		}
		router, h := newOrgRouter(store, nil)
		router.GET("/v1/organizations/:id/alert-recipients", h.ListOrgAlertRecipients)

		w := doRequest(router, "GET", "/v1/organizations/test-org-id/alert-recipients", "")

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "oncall@example.com")
		assert.Contains(t, w.Body.String(), "+12125551234")
	})
}

func TestDeleteOrgAlertRecipient(t *testing.T) {
	t.Run("deletes recipient (204)", func(t *testing.T) {
		store := enterpriseOrgStore()
		router, h := newOrgRouter(store, nil)
		router.DELETE("/v1/organizations/:id/alert-recipients/:recipientId", h.DeleteOrgAlertRecipient)

		w := doRequest(router, "DELETE", "/v1/organizations/test-org-id/alert-recipients/rec-1", "")

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("unknown recipient returns 404", func(t *testing.T) {
		store := enterpriseOrgStore()
		store.deleteRecipientErr = models.ErrNotFound
		router, h := newOrgRouter(store, nil)
		router.DELETE("/v1/organizations/:id/alert-recipients/:recipientId", h.DeleteOrgAlertRecipient)

		w := doRequest(router, "DELETE", "/v1/organizations/test-org-id/alert-recipients/rec-x", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
