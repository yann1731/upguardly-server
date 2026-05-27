package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/api/handlers"
	"upguardly-backend/internal/models"
)

const testUserID = "test-user-id"

// mockStore is a configurable in-memory implementation of models.Store.
type mockStore struct {
	// return values
	monitorResult  *models.Monitor
	monitorErr     error
	monitorsResult []models.Monitor
	monitorsErr    error
	resultsResult  []models.MonitorResult
	resultsErr     error
	alertResult    *models.Alert
	alertErr       error
	alertsResult   []models.Alert
	alertsErr      error
	deleteErr      error

	// captured call args
	lastLimit int
}

func (m *mockStore) CreateMonitor(_ context.Context, _, _, _, _ string, _, _ int, _ bool) (*models.Monitor, error) {
	return m.monitorResult, m.monitorErr
}
func (m *mockStore) ListMonitors(_ context.Context, _ string) ([]models.Monitor, error) {
	return m.monitorsResult, m.monitorsErr
}
func (m *mockStore) GetMonitor(_ context.Context, _, _ string) (*models.Monitor, error) {
	return m.monitorResult, m.monitorErr
}
func (m *mockStore) UpdateMonitor(_ context.Context, _, _ string, _ models.UpdateMonitorRequest) (*models.Monitor, error) {
	return m.monitorResult, m.monitorErr
}
func (m *mockStore) DeleteMonitor(_ context.Context, _, _ string) error {
	return m.deleteErr
}
func (m *mockStore) GetMonitorResults(_ context.Context, _, _ string, limit int) ([]models.MonitorResult, error) {
	m.lastLimit = limit
	return m.resultsResult, m.resultsErr
}
func (m *mockStore) CreateAlert(_ context.Context, _, _, _, _ string, _ bool) (*models.Alert, error) {
	return m.alertResult, m.alertErr
}
func (m *mockStore) ListAlerts(_ context.Context, _, _ string) ([]models.Alert, error) {
	return m.alertsResult, m.alertsErr
}
func (m *mockStore) GetAlert(_ context.Context, _ string) (*models.Alert, error) {
	return m.alertResult, m.alertErr
}
func (m *mockStore) UpdateAlert(_ context.Context, _ string, _ models.UpdateAlertRequest) (*models.Alert, error) {
	return m.alertResult, m.alertErr
}
func (m *mockStore) DeleteAlert(_ context.Context, _ string) error {
	return m.deleteErr
}

// ── Organization stubs ────────────────────────────────────────────────────────

func (m *mockStore) CreateOrganization(_ context.Context, _, _ string) (*models.Organization, error) {
	return nil, nil
}
func (m *mockStore) GetOrganization(_ context.Context, _ string) (*models.Organization, error) {
	return nil, models.ErrNotFound
}
func (m *mockStore) ListOrganizations(_ context.Context, _ string) ([]models.Organization, error) {
	return nil, nil
}
func (m *mockStore) UpdateOrganization(_ context.Context, _ string, _ models.UpdateOrgRequest) (*models.Organization, error) {
	return nil, nil
}
func (m *mockStore) DeleteOrganization(_ context.Context, _ string) error { return nil }

// ── Member stubs ──────────────────────────────────────────────────────────────

func (m *mockStore) GetMembership(_ context.Context, _, _ string) (*models.OrganizationMember, error) {
	return nil, models.ErrNotFound
}
func (m *mockStore) ListMembers(_ context.Context, _ string) ([]models.OrganizationMember, error) {
	return nil, nil
}
func (m *mockStore) UpdateMemberRole(_ context.Context, _, _ string, _ models.OrgRole) (*models.OrganizationMember, error) {
	return nil, nil
}
func (m *mockStore) RemoveMember(_ context.Context, _, _ string) error { return nil }

// ── Invitation stubs ──────────────────────────────────────────────────────────

func (m *mockStore) CreateInvitation(_ context.Context, _, _, _ string, _ models.OrgRole, _ string, _ time.Time) (*models.Invitation, error) {
	return nil, nil
}
func (m *mockStore) GetInvitationByToken(_ context.Context, _ string) (*models.Invitation, error) {
	return nil, models.ErrNotFound
}
func (m *mockStore) GetInvitationByID(_ context.Context, _ string) (*models.Invitation, error) {
	return nil, models.ErrNotFound
}
func (m *mockStore) ListInvitations(_ context.Context, _ string) ([]models.Invitation, error) {
	return nil, nil
}
func (m *mockStore) AcceptInvitation(_ context.Context, _, _ string) (*models.OrganizationMember, error) {
	return nil, nil
}
func (m *mockStore) RevokeInvitation(_ context.Context, _ string) error { return nil }

// ── Subscription stubs ────────────────────────────────────────────────────────

func (m *mockStore) GetSubscription(_ context.Context, _ string) (*models.Subscription, error) {
	return nil, models.ErrNotFound
}
func (m *mockStore) UpsertSubscription(_ context.Context, _ models.UpsertSubscriptionParams) (*models.Subscription, error) {
	return nil, nil
}

// newTestRouter returns a Gin router with userId pre-injected (bypasses real auth middleware).
func newTestRouter(store *mockStore) (*gin.Engine, *handlers.Handlers) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userId", testUserID)
		c.Next()
	})
	h := handlers.NewHandlers(store, nil, nil)
	return r, h
}

func doRequest(router *gin.Engine, method, path string, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, jsonReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	router.ServeHTTP(w, req)
	return w
}
