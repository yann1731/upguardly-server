package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/models"
)

// Workspace-scoped behavior: collection routes run in the X-Workspace-Id
// workspace, and org monitors are read-only to VIEWERs.

// doWorkspaceRequest is doRequest with an X-Workspace-Id header ("" = none).
func doWorkspaceRequest(router *gin.Engine, method, path, body, workspace string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if body != "" {
		req = httptest.NewRequest(method, path, jsonReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if workspace != "" {
		req.Header.Set(middleware.WorkspaceHeader, workspace)
	}
	router.ServeHTTP(w, req)
	return w
}

func anOrgMonitor() *models.Monitor {
	m := aMonitor()
	org := testOrgID
	m.OrgID = &org
	return m
}

func aMembershipWithRole(role models.OrgRole) *models.OrganizationMember {
	m := aMembership()
	m.Role = role
	return m
}

func TestWorkspaceListMonitors(t *testing.T) {
	list := func(t *testing.T, store *mockStore, workspace string) *httptest.ResponseRecorder {
		t.Helper()
		router, h := newTestRouter(store)
		router.GET("/v1/monitors", middleware.ResolveWorkspace(store), h.ListMonitors)
		return doWorkspaceRequest(router, "GET", "/v1/monitors", "", workspace)
	}

	t.Run("no header lists the personal workspace", func(t *testing.T) {
		store := &mockStore{monitorsResult: []models.Monitor{}}
		w := list(t, store, "")

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastListOrgID)
		assert.Equal(t, "", *store.lastListOrgID)
	})

	t.Run("org header lists the org workspace", func(t *testing.T) {
		store := &mockStore{monitorsResult: []models.Monitor{*anOrgMonitor()}, membershipResult: aMembershipWithRole(models.OrgRoleViewer)}
		w := list(t, store, testOrgID)

		assert.Equal(t, http.StatusOK, w.Code)
		require.NotNil(t, store.lastListOrgID)
		assert.Equal(t, testOrgID, *store.lastListOrgID)
	})

	t.Run("an org the caller doesn't belong to is refused", func(t *testing.T) {
		store := &mockStore{membershipErr: models.ErrNotFound}
		w := list(t, store, "someone-elses-org")

		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), "workspace_forbidden")
		assert.Nil(t, store.lastListOrgID)
	})
}

func TestWorkspaceCreateMonitor(t *testing.T) {
	const body = `{"name":"x","type":"HTTP","target":"http://93.184.216.34"}`
	create := func(t *testing.T, store *mockStore, body, workspace string) *httptest.ResponseRecorder {
		t.Helper()
		router, h := newTestRouter(store)
		router.POST("/v1/monitors", middleware.ResolveWorkspace(store), h.CreateMonitor)
		return doWorkspaceRequest(router, "POST", "/v1/monitors", body, workspace)
	}

	t.Run("org workspace creates on the org owner's plan", func(t *testing.T) {
		// FREE would cap at 5; the owner's ENTERPRISE plan allows a 7th.
		store := &mockStore{
			monitorResult:    anOrgMonitor(),
			orgResult:        &models.Organization{ID: testOrgID, OwnerID: "owner-id"},
			membershipResult: aMembership(),
			subResult:        aSubscription("ENTERPRISE"),
			monitorCount:     6,
		}
		w := create(t, store, body, testOrgID)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("VIEWER can't create in the org workspace", func(t *testing.T) {
		store := &mockStore{monitorResult: anOrgMonitor(), membershipResult: aMembershipWithRole(models.OrgRoleViewer)}
		w := create(t, store, body, testOrgID)

		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Contains(t, w.Body.String(), "org_role_forbidden")
	})

	t.Run("a body orgId contradicting the workspace is rejected", func(t *testing.T) {
		store := &mockStore{monitorResult: aMonitor(), membershipResult: aMembership()}
		w := create(t, store, `{"orgId":"test-org-id","name":"x","type":"HTTP","target":"http://93.184.216.34"}`, middleware.PersonalWorkspaceID)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Contains(t, w.Body.String(), "workspace_mismatch")
	})

	t.Run("a body orgId matching the workspace is accepted", func(t *testing.T) {
		store := &mockStore{monitorResult: anOrgMonitor(), membershipResult: aMembership()}
		w := create(t, store, `{"orgId":"test-org-id","name":"x","type":"HTTP","target":"http://93.184.216.34"}`, testOrgID)

		assert.Equal(t, http.StatusCreated, w.Code)
	})
}

func TestOrgMonitorWritesByRole(t *testing.T) {
	cases := []struct {
		name   string
		status int
		role   models.OrgRole
	}{
		{"VIEWER is refused", http.StatusForbidden, models.OrgRoleViewer},
		{"MEMBER is allowed", 0, models.OrgRoleMember},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newStore := func() *mockStore {
				return &mockStore{
					monitorResult:    anOrgMonitor(),
					membershipResult: aMembershipWithRole(tc.role),
					orgResult:        &models.Organization{ID: testOrgID, OwnerID: "owner-id"},
					subResult:        aSubscription("ENTERPRISE"),
					windowResult:     aWindow(),
					channelResult:    aChannel(),
					channelSettingResult: &models.MonitorChannelSetting{
						MonitorID: "mon-1", NotificationChannelID: "chan-1", Enabled: false,
					},
				}
			}

			check := func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				if tc.status == http.StatusForbidden {
					assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
					assert.Contains(t, w.Body.String(), "org_role_forbidden")
				} else {
					assert.Less(t, w.Code, 300, w.Body.String())
				}
			}

			t.Run("update monitor", func(t *testing.T) {
				store := newStore()
				router, h := newTestRouter(store)
				router.PUT("/v1/monitors/:id", h.UpdateMonitor)
				check(t, doRequest(router, "PUT", "/v1/monitors/mon-1", `{"name":"renamed"}`))
			})

			t.Run("delete monitor", func(t *testing.T) {
				store := newStore()
				router, h := newTestRouter(store)
				router.DELETE("/v1/monitors/:id", h.DeleteMonitor)
				check(t, doRequest(router, "DELETE", "/v1/monitors/mon-1", ""))
			})

			t.Run("create maintenance window", func(t *testing.T) {
				store := newStore()
				router, h := newTestRouter(store)
				router.POST("/v1/monitors/:id/maintenance-windows", h.CreateMaintenanceWindow)
				check(t, doRequest(router, "POST", "/v1/monitors/mon-1/maintenance-windows",
					`{"kind":"ONE_OFF","startsAt":"2026-08-01T00:00:00Z","endsAt":"2026-08-01T02:00:00Z"}`))
			})

			t.Run("delete maintenance window", func(t *testing.T) {
				store := newStore()
				router, h := newTestRouter(store)
				router.DELETE("/v1/monitors/:id/maintenance-windows/:windowId", h.DeleteMaintenanceWindow)
				check(t, doRequest(router, "DELETE", "/v1/monitors/mon-1/maintenance-windows/win-1", ""))
			})

			t.Run("set monitor channel", func(t *testing.T) {
				store := newStore()
				router, h := newTestRouter(store)
				router.PUT("/v1/monitors/:id/channels/:channelId", h.SetMonitorChannel)
				check(t, doRequest(router, "PUT", "/v1/monitors/mon-1/channels/chan-1", `{"enabled":false}`))
			})

			t.Run("reset monitor channel", func(t *testing.T) {
				store := newStore()
				router, h := newTestRouter(store)
				router.DELETE("/v1/monitors/:id/channels/:channelId", h.DeleteMonitorChannel)
				check(t, doRequest(router, "DELETE", "/v1/monitors/mon-1/channels/chan-1", ""))
			})
		})
	}

	t.Run("VIEWER can still read the org monitor", func(t *testing.T) {
		store := &mockStore{monitorResult: anOrgMonitor(), membershipResult: aMembershipWithRole(models.OrgRoleViewer)}
		router, h := newTestRouter(store)
		router.GET("/v1/monitors/:id", h.GetMonitor)

		w := doRequest(router, "GET", "/v1/monitors/mon-1", "")

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestWorkspaceNotificationChannels(t *testing.T) {
	ownerChannels := func() []models.NotificationChannel {
		return []models.NotificationChannel{
			{ID: "c1", Channel: models.AlertChannelEMAIL, Target: "owner@example.com", Enabled: true},
			{ID: "c2", Channel: models.AlertChannelSMS, Target: "+15551234567", Enabled: true},
			{ID: "c3", Channel: models.AlertChannelDISCORD, Target: "https://discord.com/api/webhooks/1/s3cret", Enabled: true},
		}
	}
	list := func(t *testing.T, store *mockStore, workspace string) []models.NotificationChannel {
		t.Helper()
		router, h := newTestRouter(store)
		router.GET("/v1/notification-channels", middleware.ResolveWorkspace(store), h.ListNotificationChannels)

		w := doWorkspaceRequest(router, "GET", "/v1/notification-channels", "", workspace)

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var got []models.NotificationChannel
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		return got
	}

	t.Run("members see the owner's integrations masked", func(t *testing.T) {
		store := &mockStore{
			channelsResult:   ownerChannels(),
			membershipResult: aMembership(),
			orgResult:        &models.Organization{ID: testOrgID, OwnerID: "owner-id"},
		}
		got := list(t, store, testOrgID)

		require.Len(t, got, 3)
		assert.Equal(t, "o***@example.com", got[0].Target)
		assert.Equal(t, "***4567", got[1].Target)
		assert.Equal(t, "https://discord.com/***", got[2].Target)
	})

	t.Run("the owner sees their integrations unmasked", func(t *testing.T) {
		store := &mockStore{
			channelsResult:   ownerChannels(),
			membershipResult: aMembershipWithRole(models.OrgRoleOwner),
			orgResult:        &models.Organization{ID: testOrgID, OwnerID: testUserID},
		}
		got := list(t, store, testOrgID)

		assert.Equal(t, ownerChannels()[2].Target, got[2].Target)
	})

	t.Run("the personal workspace is unmasked", func(t *testing.T) {
		got := list(t, &mockStore{channelsResult: ownerChannels()}, middleware.PersonalWorkspaceID)

		assert.Equal(t, "owner@example.com", got[0].Target)
	})

	t.Run("members can't change the owner's integrations", func(t *testing.T) {
		for _, tc := range []struct{ method, pattern, path, body string }{
			{"POST", "/v1/notification-channels", "/v1/notification-channels", `{"channel":"EMAIL"}`},
			{"PUT", "/v1/notification-channels/:id", "/v1/notification-channels/c1", `{"enabled":false}`},
			{"DELETE", "/v1/notification-channels/:id", "/v1/notification-channels/c1", ""},
		} {
			store := &mockStore{
				channelResult:    aChannel(),
				membershipResult: aMembershipWithRole(models.OrgRoleAdmin),
				orgResult:        &models.Organization{ID: testOrgID, OwnerID: "owner-id"},
			}
			router, h := newTestRouter(store)
			h.UserEmailLookup = func(string) (string, error) { return stubbedEmail, nil }
			handler := map[string]gin.HandlerFunc{
				"POST": h.CreateNotificationChannel, "PUT": h.UpdateNotificationChannel, "DELETE": h.DeleteNotificationChannel,
			}[tc.method]
			router.Handle(tc.method, tc.pattern, middleware.ResolveWorkspace(store), handler)

			w := doWorkspaceRequest(router, tc.method, tc.path, tc.body, testOrgID)

			assert.Equal(t, http.StatusForbidden, w.Code, tc.method)
			assert.Contains(t, w.Body.String(), "org_owner_only", tc.method)
		}
	})
}
