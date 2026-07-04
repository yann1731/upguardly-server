package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"upguardly-backend/internal/api/handlers"
	"upguardly-backend/internal/models"
)

// newRegionRouter is newTestRouter with a caller-chosen available-region set,
// for exercising the deployment-availability gate.
func newRegionRouter(store *mockStore, available []string) (*gin.Engine, *handlers.Handlers) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userId", testUserID)
		c.Next()
	})
	h := handlers.NewHandlers(store, nil, nil, available)
	return r, h
}

func TestListRegions(t *testing.T) {
	t.Run("returns only available regions with display names", func(t *testing.T) {
		router, h := newRegionRouter(&mockStore{}, []string{"na-east", "eu-west"})
		router.GET("/v1/regions", h.ListRegions)

		w := doRequest(router, "GET", "/v1/regions", "")

		assert.Equal(t, http.StatusOK, w.Code)
		var got []models.Region
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		require.Len(t, got, 2)
		assert.Equal(t, models.Region{ID: "na-east", Name: "North America (East)"}, got[0])
		assert.Equal(t, models.Region{ID: "eu-west", Name: "EU West"}, got[1])
	})
}

func TestCreateMonitorRegionAvailability(t *testing.T) {
	t.Run("registry region without a deployed pool returns 400", func(t *testing.T) {
		// eu-west is a valid registry region but not in AVAILABLE_REGIONS:
		// selecting it must fail rather than silently never reach quorum.
		store := &mockStore{monitorResult: aMonitor(), subResult: aSubscription("PRO")}
		router, h := newRegionRouter(store, []string{"na-east"})
		router.POST("/v1/monitors", h.CreateMonitor)

		w := doRequest(router, "POST", "/v1/monitors", `{"name":"x","type":"HTTP","target":"http://93.184.216.34","regions":["eu-west"]}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestGetMonitorRegions(t *testing.T) {
	t.Run("returns per-region status", func(t *testing.T) {
		code := 200
		store := &mockStore{regionStatusResult: []models.MonitorRegionStatus{
			{Region: "eu-west", Status: models.StatusUP, Latency: 42, StatusCode: &code, CheckedAt: time.Now(), Stale: false},
			{Region: "na-east", Status: models.StatusDOWN, Latency: 0, CheckedAt: time.Now().Add(-time.Hour), Stale: true},
		}}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/regions", h.GetMonitorRegions)

		w := doRequest(router, "GET", "/v1/monitors/mon-1/regions", "")

		assert.Equal(t, http.StatusOK, w.Code)
		var got []models.MonitorRegionStatus
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		require.Len(t, got, 2)
		assert.Equal(t, "eu-west", got[0].Region)
		assert.False(t, got[0].Stale)
		assert.True(t, got[1].Stale)
	})

	t.Run("monitor not found returns 404", func(t *testing.T) {
		store := &mockStore{regionStatusErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/regions", h.GetMonitorRegions)

		w := doRequest(router, "GET", "/v1/monitors/missing/regions", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

func TestGetMonitorResultsRegionFilter(t *testing.T) {
	t.Run("passes ?region= through to the store", func(t *testing.T) {
		store := &mockStore{resultsResult: []models.MonitorResult{}}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/results", h.GetMonitorResults)

		w := doRequest(router, "GET", "/v1/monitors/mon-1/results?region=eu-west", "")

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "eu-west", store.lastResultsRegion)
	})

	t.Run("unknown region returns 400", func(t *testing.T) {
		store := &mockStore{resultsResult: []models.MonitorResult{}}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id/results", h.GetMonitorResults)

		w := doRequest(router, "GET", "/v1/monitors/mon-1/results?region=mars-north", "")

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}
