package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"upguardly-backend/internal/models"
)

func TestCreateMonitor(t *testing.T) {
	t.Run("valid body returns 201 with monitor", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), membershipResult: aMembership()}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors", h.CreateMonitor)

		w := doRequest(router, "POST", "/v1/monitors", `{"orgId":"test-org-id","name":"My Monitor","type":"HTTP","target":"https://example.com"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
		var got models.Monitor
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Equal(t, "mon-1", got.ID)
	})

	t.Run("missing required field returns 400", func(t *testing.T) {
		store := &mockStore{}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors", h.CreateMonitor)

		w := doRequest(router, "POST", "/v1/monitors", `{"orgId":"test-org-id","type":"HTTP","target":"https://example.com"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("missing orgId creates a solo monitor (201)", func(t *testing.T) {
		// A monitor with no org is a solo (FREE/PRO) monitor owned by the user.
		store := &mockStore{monitorResult: aMonitor()}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors", h.CreateMonitor)

		w := doRequest(router, "POST", "/v1/monitors", `{"name":"x","type":"HTTP","target":"https://example.com"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("invalid type returns 400", func(t *testing.T) {
		store := &mockStore{}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors", h.CreateMonitor)

		w := doRequest(router, "POST", "/v1/monitors", `{"orgId":"test-org-id","name":"x","type":"INVALID","target":"https://example.com"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("non-member returns 403", func(t *testing.T) {
		store := &mockStore{membershipErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors", h.CreateMonitor)

		w := doRequest(router, "POST", "/v1/monitors", `{"orgId":"test-org-id","name":"x","type":"HTTP","target":"https://example.com"}`)

		assert.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("monitor limit reached returns 402", func(t *testing.T) {
		// FREE plan caps at 5 monitors; org already has 5.
		store := &mockStore{membershipResult: aMembership(), monitorCount: 5}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors", h.CreateMonitor)

		w := doRequest(router, "POST", "/v1/monitors", `{"orgId":"test-org-id","name":"x","type":"HTTP","target":"https://example.com"}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
	})

	t.Run("PRO plan allows beyond free limit", func(t *testing.T) {
		// PRO caps at 50; org has 6 monitors — should succeed. The org's plan is
		// its owner's plan, so the org resolves to its owner (testUserID) → PRO.
		store := &mockStore{
			monitorResult:    aMonitor(),
			membershipResult: aMembership(),
			orgResult:        &models.Organization{ID: "test-org-id", OwnerID: testUserID},
			subResult:        aSubscription("PRO"),
			monitorCount:     6,
		}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors", h.CreateMonitor)

		w := doRequest(router, "POST", "/v1/monitors", `{"orgId":"test-org-id","name":"x","type":"HTTP","target":"https://example.com"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
	})
}

func TestListMonitors(t *testing.T) {
	t.Run("returns all user monitors", func(t *testing.T) {
		store := &mockStore{monitorsResult: []models.Monitor{*aMonitor(), *aMonitor()}}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors", h.ListMonitors)

		w := doRequest(router, "GET", "/v1/monitors", "")

		assert.Equal(t, http.StatusOK, w.Code)
		var got []models.Monitor
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		assert.Len(t, got, 2)
	})

	t.Run("returns empty array when no monitors", func(t *testing.T) {
		store := &mockStore{monitorsResult: []models.Monitor{}}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors", h.ListMonitors)

		w := doRequest(router, "GET", "/v1/monitors", "")

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Contains(t, w.Body.String(), "[]")
	})
}

func TestGetMonitor(t *testing.T) {
	t.Run("found returns 200", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor()}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id", h.GetMonitor)

		w := doRequest(router, "GET", "/v1/monitors/mon-1", "")

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		store := &mockStore{monitorErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id", h.GetMonitor)

		w := doRequest(router, "GET", "/v1/monitors/missing", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestUpdateMonitor(t *testing.T) {
	t.Run("valid partial body returns 200", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor()}
		router, h := newTestRouter(store)
		router.PUT("/v1/monitors/:id", h.UpdateMonitor)

		w := doRequest(router, "PUT", "/v1/monitors/mon-1", `{"name":"Updated"}`)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("empty body returns 400", func(t *testing.T) {
		store := &mockStore{}
		router, h := newTestRouter(store)
		router.PUT("/v1/monitors/:id", h.UpdateMonitor)

		w := doRequest(router, "PUT", "/v1/monitors/mon-1", `{}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("monitor not found returns 404", func(t *testing.T) {
		store := &mockStore{monitorErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.PUT("/v1/monitors/:id", h.UpdateMonitor)

		w := doRequest(router, "PUT", "/v1/monitors/missing", `{"name":"Updated"}`)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestDeleteMonitor(t *testing.T) {
	t.Run("found returns 204", func(t *testing.T) {
		store := &mockStore{}
		router, h := newTestRouter(store)
		router.DELETE("/v1/monitors/:id", h.DeleteMonitor)

		w := doRequest(router, "DELETE", "/v1/monitors/mon-1", "")

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		store := &mockStore{deleteErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.DELETE("/v1/monitors/:id", h.DeleteMonitor)

		w := doRequest(router, "DELETE", "/v1/monitors/missing", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestGetMonitorResults(t *testing.T) {
	t.Run("returns results with default limit 100", func(t *testing.T) {
		store := &mockStore{resultsResult: []models.MonitorResult{}}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/results", h.GetMonitorResults)

		doRequest(router, "GET", "/v1/monitors/mon-1/results", "")

		assert.Equal(t, 100, store.lastLimit)
	})

	t.Run("respects ?limit=500", func(t *testing.T) {
		store := &mockStore{resultsResult: []models.MonitorResult{}}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/results", h.GetMonitorResults)

		doRequest(router, "GET", "/v1/monitors/mon-1/results?limit=500", "")

		assert.Equal(t, 500, store.lastLimit)
	})

	t.Run("monitor not found returns 404", func(t *testing.T) {
		store := &mockStore{resultsErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/results", h.GetMonitorResults)

		w := doRequest(router, "GET", "/v1/monitors/missing/results", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestGetMonitorIncidents(t *testing.T) {
	t.Run("returns incidents with default limit 100", func(t *testing.T) {
		store := &mockStore{incidentsResult: []models.Incident{}}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/incidents", h.GetMonitorIncidents)

		w := doRequest(router, "GET", "/v1/monitors/mon-1/incidents", "")

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, 100, store.lastLimit)
	})

	t.Run("respects ?limit=10", func(t *testing.T) {
		store := &mockStore{incidentsResult: []models.Incident{}}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/incidents", h.GetMonitorIncidents)

		doRequest(router, "GET", "/v1/monitors/mon-1/incidents?limit=10", "")

		assert.Equal(t, 10, store.lastLimit)
	})

	t.Run("monitor not found returns 404", func(t *testing.T) {
		store := &mockStore{incidentsErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/incidents", h.GetMonitorIncidents)

		w := doRequest(router, "GET", "/v1/monitors/missing/incidents", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestGetMonitorStats(t *testing.T) {
	t.Run("returns stats", func(t *testing.T) {
		store := &mockStore{statsResult: &models.MonitorStats{AvgLatency: 42}}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/stats", h.GetMonitorStats)

		w := doRequest(router, "GET", "/v1/monitors/mon-1/stats?period=7d", "")

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("monitor not found returns 404", func(t *testing.T) {
		store := &mockStore{statsErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/stats", h.GetMonitorStats)

		w := doRequest(router, "GET", "/v1/monitors/missing/stats", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
