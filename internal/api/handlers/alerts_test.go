package handlers_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"upguardly-backend/internal/models"
)

func TestCreateAlert(t *testing.T) {
	t.Run("valid body returns 201", func(t *testing.T) {
		store := &mockStore{alertResult: anAlert()}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors/:id/alerts", h.CreateAlert)

		w := doRequest(router, "POST", "/v1/monitors/mon-1/alerts", `{"channel":"EMAIL","target":"user@example.com"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("monitor not found returns 404", func(t *testing.T) {
		store := &mockStore{alertErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors/:id/alerts", h.CreateAlert)

		w := doRequest(router, "POST", "/v1/monitors/missing/alerts", `{"channel":"EMAIL","target":"user@example.com"}`)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("missing required field returns 400", func(t *testing.T) {
		store := &mockStore{}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors/:id/alerts", h.CreateAlert)

		w := doRequest(router, "POST", "/v1/monitors/mon-1/alerts", `{"channel":"EMAIL"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestListAlerts(t *testing.T) {
	t.Run("returns alerts for monitor", func(t *testing.T) {
		store := &mockStore{alertsResult: []models.Alert{*anAlert()}}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/alerts", h.ListAlerts)

		w := doRequest(router, "GET", "/v1/monitors/mon-1/alerts", "")

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("monitor not found returns 404", func(t *testing.T) {
		store := &mockStore{alertsErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/alerts", h.ListAlerts)

		w := doRequest(router, "GET", "/v1/monitors/missing/alerts", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestUpdateAlert(t *testing.T) {
	t.Run("valid update returns 200", func(t *testing.T) {
		// GetAlert succeeds, GetMonitor succeeds, UpdateAlert succeeds
		store := &mockStore{alertResult: anAlert(), monitorResult: aMonitor()}
		router, h := newTestRouter(store)
		router.PUT("/v1/alerts/:id", h.UpdateAlert)

		w := doRequest(router, "PUT", "/v1/alerts/alert-1", `{"target":"new@example.com"}`)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("alert not found returns 404", func(t *testing.T) {
		store := &mockStore{alertErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.PUT("/v1/alerts/:id", h.UpdateAlert)

		w := doRequest(router, "PUT", "/v1/alerts/missing", `{"target":"new@example.com"}`)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("wrong owner returns 404", func(t *testing.T) {
		// Alert exists but monitor belongs to another user
		store := &mockStore{alertResult: anAlert(), monitorErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.PUT("/v1/alerts/:id", h.UpdateAlert)

		w := doRequest(router, "PUT", "/v1/alerts/alert-1", `{"target":"new@example.com"}`)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("empty body returns 400", func(t *testing.T) {
		store := &mockStore{}
		router, h := newTestRouter(store)
		router.PUT("/v1/alerts/:id", h.UpdateAlert)

		w := doRequest(router, "PUT", "/v1/alerts/alert-1", `{}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestDeleteAlert(t *testing.T) {
	t.Run("found with correct owner returns 204", func(t *testing.T) {
		store := &mockStore{alertResult: anAlert(), monitorResult: aMonitor()}
		router, h := newTestRouter(store)
		router.DELETE("/v1/alerts/:id", h.DeleteAlert)

		w := doRequest(router, "DELETE", "/v1/alerts/alert-1", "")

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("alert not found returns 404", func(t *testing.T) {
		store := &mockStore{alertErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.DELETE("/v1/alerts/:id", h.DeleteAlert)

		w := doRequest(router, "DELETE", "/v1/alerts/missing", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("wrong owner returns 404", func(t *testing.T) {
		store := &mockStore{alertResult: anAlert(), monitorErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.DELETE("/v1/alerts/:id", h.DeleteAlert)

		w := doRequest(router, "DELETE", "/v1/alerts/alert-1", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
