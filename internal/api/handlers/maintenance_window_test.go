package handlers_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"upguardly-backend/internal/models"
)

func aWindow() *models.MaintenanceWindow {
	return &models.MaintenanceWindow{ID: "win-1", MonitorID: "mon-1", Kind: models.MaintenanceWindowOneOff}
}

func TestCreateMaintenanceWindow(t *testing.T) {
	oneOffBody := `{"kind":"ONE_OFF","startsAt":"2026-08-01T00:00:00Z","endsAt":"2026-08-01T02:00:00Z"}`

	t.Run("ENTERPRISE can create a one-off window", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), windowResult: aWindow(), subResult: aSubscription("ENTERPRISE")}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors/:id/maintenance-windows", h.CreateMaintenanceWindow)

		w := doRequest(router, "POST", "/v1/monitors/mon-1/maintenance-windows", oneOffBody)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("non-ENTERPRISE returns 402", func(t *testing.T) {
		for _, plan := range []string{"FREE", "PRO"} {
			store := &mockStore{monitorResult: aMonitor(), windowResult: aWindow()}
			if plan != "FREE" {
				store.subResult = aSubscription(plan)
			}
			router, h := newTestRouter(store)
			router.POST("/v1/monitors/:id/maintenance-windows", h.CreateMaintenanceWindow)

			w := doRequest(router, "POST", "/v1/monitors/mon-1/maintenance-windows", oneOffBody)

			assert.Equal(t, http.StatusPaymentRequired, w.Code, plan)
		}
	})

	t.Run("weekly window validates and stores", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), windowResult: aWindow(), subResult: aSubscription("ENTERPRISE")}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors/:id/maintenance-windows", h.CreateMaintenanceWindow)

		w := doRequest(router, "POST", "/v1/monitors/mon-1/maintenance-windows",
			`{"kind":"WEEKLY","weekday":2,"startTime":"03:00","durationMinutes":60,"timezone":"America/Toronto"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
		if assert.NotNil(t, store.lastCreateWindow) {
			assert.Equal(t, models.MaintenanceWindowWeekly, store.lastCreateWindow.Kind)
		}
	})

	t.Run("unknown timezone returns 400", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), windowResult: aWindow(), subResult: aSubscription("ENTERPRISE")}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors/:id/maintenance-windows", h.CreateMaintenanceWindow)

		w := doRequest(router, "POST", "/v1/monitors/mon-1/maintenance-windows",
			`{"kind":"WEEKLY","weekday":2,"startTime":"03:00","durationMinutes":60,"timezone":"Not/AZone"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("one-off with inverted range returns 400", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), windowResult: aWindow(), subResult: aSubscription("ENTERPRISE")}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors/:id/maintenance-windows", h.CreateMaintenanceWindow)

		w := doRequest(router, "POST", "/v1/monitors/mon-1/maintenance-windows",
			`{"kind":"ONE_OFF","startsAt":"2026-08-01T02:00:00Z","endsAt":"2026-08-01T00:00:00Z"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("monitor not found returns 404", func(t *testing.T) {
		store := &mockStore{monitorErr: models.ErrNotFound, subResult: aSubscription("ENTERPRISE")}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors/:id/maintenance-windows", h.CreateMaintenanceWindow)

		w := doRequest(router, "POST", "/v1/monitors/missing/maintenance-windows", oneOffBody)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestDeleteMaintenanceWindow(t *testing.T) {
	t.Run("delete succeeds regardless of plan", func(t *testing.T) {
		// FREE (no sub): removing suppression must never be gated.
		store := &mockStore{monitorResult: aMonitor()}
		router, h := newTestRouter(store)
		router.DELETE("/v1/monitors/:id/maintenance-windows/:windowId", h.DeleteMaintenanceWindow)

		w := doRequest(router, "DELETE", "/v1/monitors/mon-1/maintenance-windows/win-1", "")

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("missing window returns 404", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), deleteWindowErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.DELETE("/v1/monitors/:id/maintenance-windows/:windowId", h.DeleteMaintenanceWindow)

		w := doRequest(router, "DELETE", "/v1/monitors/mon-1/maintenance-windows/nope", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
