package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v76"

	"upguardly-backend/internal/api/handlers"
	"upguardly-backend/internal/models"
)

const testUserID = "test-user-id"

// mockStore is a configurable in-memory implementation of models.Store.
type mockStore struct {
	// return values
	monitorResult   *models.Monitor
	monitorErr      error
	monitorsResult  []models.Monitor
	monitorsErr     error
	monitorCount    int
	monitorCountErr error
	resultsResult   []models.MonitorResult
	resultsErr      error
	incidentsResult []models.Incident
	incidentsErr    error
	statsResult     *models.MonitorStats
	statsErr        error
	alertResult     *models.Alert
	alertErr        error
	alertsResult    []models.Alert
	alertsErr       error
	deleteErr       error

	// org / subscription / membership return values
	orgResult        *models.Organization
	orgErr           error
	orgsResult       []models.Organization
	orgsErr          error
	createOrgErr     error
	membershipResult *models.OrganizationMember
	membershipErr    error
	inviteResult     *models.Invitation
	inviteErr        error
	acceptInviteErr  error
	subResult        *models.Subscription
	subErr           error
	deleteOrgErr     error

	// captured call args
	lastLimit       int
	lastUpsertSub   *models.UpsertSubscriptionParams
	deleteOrgCalled bool
}

func (m *mockStore) CreateMonitor(_ context.Context, _, _, _, _, _ string, _, _ int, _ bool) (*models.Monitor, error) {
	return m.monitorResult, m.monitorErr
}
func (m *mockStore) CountMonitorsByOrg(_ context.Context, _ string) (int, error) {
	return m.monitorCount, m.monitorCountErr
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
func (m *mockStore) ListIncidents(_ context.Context, _, _ string, limit int) ([]models.Incident, error) {
	m.lastLimit = limit
	return m.incidentsResult, m.incidentsErr
}
func (m *mockStore) GetMonitorStats(_ context.Context, _, _ string, _ time.Time) (*models.MonitorStats, error) {
	return m.statsResult, m.statsErr
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
	return m.orgResult, m.createOrgErr
}
func (m *mockStore) GetOrganization(_ context.Context, _ string) (*models.Organization, error) {
	if m.orgResult == nil && m.orgErr == nil {
		return nil, models.ErrNotFound
	}
	return m.orgResult, m.orgErr
}
func (m *mockStore) ListOrganizations(_ context.Context, _ string) ([]models.Organization, error) {
	return m.orgsResult, m.orgsErr
}
func (m *mockStore) UpdateOrganization(_ context.Context, _ string, _ models.UpdateOrgRequest) (*models.Organization, error) {
	return nil, nil
}
func (m *mockStore) DeleteOrganization(_ context.Context, _ string) error {
	m.deleteOrgCalled = true
	return m.deleteOrgErr
}

// ── Member stubs ──────────────────────────────────────────────────────────────

func (m *mockStore) GetMembership(_ context.Context, _, _ string) (*models.OrganizationMember, error) {
	return m.membershipResult, m.membershipErr
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
	if m.inviteResult == nil && m.inviteErr == nil {
		return nil, models.ErrNotFound
	}
	return m.inviteResult, m.inviteErr
}
func (m *mockStore) GetInvitationByID(_ context.Context, _ string) (*models.Invitation, error) {
	return nil, models.ErrNotFound
}
func (m *mockStore) ListInvitations(_ context.Context, _ string) ([]models.Invitation, error) {
	return nil, nil
}
func (m *mockStore) AcceptInvitation(_ context.Context, _, _ string) (*models.OrganizationMember, error) {
	return m.membershipResult, m.acceptInviteErr
}
func (m *mockStore) RevokeInvitation(_ context.Context, _ string) error { return nil }

// ── Subscription stubs ────────────────────────────────────────────────────────

func (m *mockStore) GetSubscription(_ context.Context, _ string) (*models.Subscription, error) {
	if m.subResult == nil && m.subErr == nil {
		return nil, models.ErrNotFound
	}
	return m.subResult, m.subErr
}
func (m *mockStore) UpsertSubscription(_ context.Context, params models.UpsertSubscriptionParams) (*models.Subscription, error) {
	m.lastUpsertSub = &params
	return m.subResult, m.subErr
}

const testOrgID = "test-org-id"

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

// newOrgRouter pre-injects both userId and orgId (bypasses real auth + org-role
// middleware) and wires the given StripeService (nil = billing not configured).
func newOrgRouter(store *mockStore, s handlers.StripeService) (*gin.Engine, *handlers.Handlers) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userId", testUserID)
		c.Set("orgId", testOrgID)
		c.Next()
	})
	h := handlers.NewHandlers(store, nil, s)
	return r, h
}

// fakeStripe is a configurable StripeService for billing handler tests.
type fakeStripe struct {
	proPriceID  string
	entPriceID  string
	customerID  string
	checkoutURL string
	portalURL   string
	event       stripe.Event
	parseErr    error
	ensureErr   error
	checkoutErr error
	portalErr   error
	getSub      *stripe.Subscription
	getSubErr   error

	// Captured calls for assertions.
	setOrgIDCustomer string
	setOrgIDOrg      string
	lastCheckoutMeta map[string]string
}

func (f *fakeStripe) PriceIDForPlan(plan string) (string, error) {
	switch plan {
	case "PRO":
		return f.proPriceID, nil
	case "ENTERPRISE":
		return f.entPriceID, nil
	default:
		return "", nil
	}
}
func (f *fakeStripe) EnsureCustomer(_, _ string) (string, error) {
	return f.customerID, f.ensureErr
}
func (f *fakeStripe) EnsureCustomerForUser(_, _ string) (string, error) {
	return f.customerID, f.ensureErr
}
func (f *fakeStripe) SetCustomerOrgID(customerID, orgID string) error {
	f.setOrgIDCustomer = customerID
	f.setOrgIDOrg = orgID
	return nil
}
func (f *fakeStripe) GetSubscription(_ string) (*stripe.Subscription, error) {
	return f.getSub, f.getSubErr
}
func (f *fakeStripe) CreateCheckoutSession(_, _, _, _ string) (string, error) {
	return f.checkoutURL, f.checkoutErr
}
func (f *fakeStripe) CreateOrgCheckoutSession(_, _, _, _ string, metadata map[string]string) (string, error) {
	f.lastCheckoutMeta = metadata
	return f.checkoutURL, f.checkoutErr
}
func (f *fakeStripe) CreatePortalSession(_, _ string) (string, error) {
	return f.portalURL, f.portalErr
}
func (f *fakeStripe) ParseWebhook(_ []byte, _ string) (stripe.Event, error) {
	return f.event, f.parseErr
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
