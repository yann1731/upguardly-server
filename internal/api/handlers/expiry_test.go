package handlers_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"upguardly-backend/internal/models"
)

func TestCreateMonitorExpiryMonitoring(t *testing.T) {
	t.Run("cert check on non-ENTERPRISE returns 402", func(t *testing.T) {
		for _, plan := range []string{"FREE", "PRO"} {
			store := &mockStore{monitorResult: aMonitor()}
			if plan != "FREE" {
				store.subResult = aSubscription(plan)
			}
			router, h := newTestRouter(store)
			router.POST("/v1/monitors", h.CreateMonitor)

			w := doRequest(router, "POST", "/v1/monitors", `{"name":"x","type":"HTTP","target":"https://93.184.216.34","certCheckEnabled":true}`)

			assert.Equal(t, http.StatusPaymentRequired, w.Code, plan)
		}
	})

	t.Run("cert and domain check on ENTERPRISE returns 201", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), subResult: aSubscription("ENTERPRISE")}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors", h.CreateMonitor)

		w := doRequest(router, "POST", "/v1/monitors", `{"name":"x","type":"HTTP","target":"https://93.184.216.34","certCheckEnabled":true,"domainCheckEnabled":true}`)

		assert.Equal(t, http.StatusCreated, w.Code)
		if assert.NotNil(t, store.lastUpdateReq) {
			if assert.NotNil(t, store.lastUpdateReq.CertCheckEnabled) {
				assert.True(t, *store.lastUpdateReq.CertCheckEnabled)
			}
			if assert.NotNil(t, store.lastUpdateReq.DomainCheckEnabled) {
				assert.True(t, *store.lastUpdateReq.DomainCheckEnabled)
			}
		}
	})

	t.Run("cert check on a PORT monitor returns 400", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), subResult: aSubscription("ENTERPRISE")}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors", h.CreateMonitor)

		w := doRequest(router, "POST", "/v1/monitors", `{"name":"x","type":"PORT","target":"example.com:5432","certCheckEnabled":true}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("out-of-bounds expiry threshold returns 400", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), subResult: aSubscription("ENTERPRISE")}
		router, h := newTestRouter(store)
		router.POST("/v1/monitors", h.CreateMonitor)

		w := doRequest(router, "POST", "/v1/monitors", `{"name":"x","type":"HTTP","target":"https://93.184.216.34","certCheckEnabled":true,"certExpiryThresholdDays":200}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestUpdateMonitorExpiryMonitoring(t *testing.T) {
	t.Run("enabling on non-ENTERPRISE returns 402", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), subResult: aSubscription("PRO")}
		router, h := newTestRouter(store)
		router.PUT("/v1/monitors/:id", h.UpdateMonitor)

		w := doRequest(router, "PUT", "/v1/monitors/mon-1", `{"certCheckEnabled":true}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
	})

	t.Run("disabling is allowed on any plan", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor()} // FREE
		router, h := newTestRouter(store)
		router.PUT("/v1/monitors/:id", h.UpdateMonitor)

		w := doRequest(router, "PUT", "/v1/monitors/mon-1", `{"certCheckEnabled":false}`)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("enabling on ENTERPRISE returns 200", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), subResult: aSubscription("ENTERPRISE")}
		router, h := newTestRouter(store)
		router.PUT("/v1/monitors/:id", h.UpdateMonitor)

		w := doRequest(router, "PUT", "/v1/monitors/mon-1", `{"domainCheckEnabled":true}`)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestGetMonitorExpiry(t *testing.T) {
	t.Run("returns the monitor's expiry status rows", func(t *testing.T) {
		expires := aMonitor().CreatedAt.AddDate(1, 0, 0)
		store := &mockStore{
			monitorResult: aMonitor(),
			expiryStatusResult: []models.MonitorExpiryStatus{
				{Kind: models.ExpiryKindCert, ExpiresAt: &expires},
			},
		}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/expiry", h.GetMonitorExpiry)

		w := doRequest(router, "GET", "/v1/monitors/mon-1/expiry", "")

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("monitor not found returns 404", func(t *testing.T) {
		store := &mockStore{monitorErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/expiry", h.GetMonitorExpiry)

		w := doRequest(router, "GET", "/v1/monitors/missing/expiry", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}
