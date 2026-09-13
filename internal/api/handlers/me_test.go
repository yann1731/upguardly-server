package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"upguardly-backend/internal/models"
)

type meBody struct {
	UserID        string             `json:"userId"`
	Email         string             `json:"email"`
	AccountType   models.AccountType `json:"accountType"`
	EffectivePlan string             `json:"effectivePlan"`
	Org           *models.AccountOrg `json:"org"`
	Workspaces    []models.Workspace `json:"workspaces"`
}

func TestGetMe(t *testing.T) {
	get := func(t *testing.T, store *mockStore) meBody {
		t.Helper()
		router, h := newTestRouter(store)
		h.UserEmailLookup = func(string) (string, error) { return "user@example.com", nil }
		router.GET("/v1/me", h.GetMe)

		w := doRequest(router, "GET", "/v1/me", "")

		require.Equal(t, http.StatusOK, w.Code)
		var got meBody
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		return got
	}

	t.Run("user without an org is INDIVIDUAL on their own plan", func(t *testing.T) {
		got := get(t, &mockStore{subResult: aSubscription("PRO")})

		assert.Equal(t, testUserID, got.UserID)
		assert.Equal(t, "user@example.com", got.Email)
		assert.Equal(t, models.AccountTypeIndividual, got.AccountType)
		assert.Equal(t, "PRO", got.EffectivePlan)
		assert.Nil(t, got.Org)
		assert.Equal(t, []models.Workspace{
			{ID: "personal", Type: models.WorkspaceTypePersonal, Plan: "PRO", MaxMonitors: 20},
		}, got.Workspaces)
	})

	t.Run("user without a subscription resolves to FREE", func(t *testing.T) {
		got := get(t, &mockStore{})

		assert.Equal(t, models.AccountTypeIndividual, got.AccountType)
		assert.Equal(t, "FREE", got.EffectivePlan)
	})

	t.Run("org creator is ORG_OWNER", func(t *testing.T) {
		owner := aMembership()
		owner.Role = models.OrgRoleOwner
		got := get(t, &mockStore{
			orgsResult:       []models.Organization{{ID: "test-org-id", Name: "Acme", OwnerID: testUserID}},
			membershipResult: owner,
			subResult:        aSubscription("ENTERPRISE"),
		})

		assert.Equal(t, models.AccountTypeOrgOwner, got.AccountType)
		assert.Equal(t, "ENTERPRISE", got.EffectivePlan)
		require.NotNil(t, got.Org)
		assert.Equal(t, models.OrgRoleOwner, got.Org.Role)
	})

	t.Run("invited user is ORG_MEMBER on the org owner's plan", func(t *testing.T) {
		got := get(t, &mockStore{
			orgsResult:       []models.Organization{{ID: "test-org-id", Name: "Acme", OwnerID: "owner-id"}},
			membershipResult: aMembership(),
			subResult:        aSubscription("ENTERPRISE"),
		})

		assert.Equal(t, models.AccountTypeOrgMember, got.AccountType)
		assert.Equal(t, "ENTERPRISE", got.EffectivePlan)
		require.NotNil(t, got.Org)
		assert.Equal(t, "test-org-id", got.Org.ID)
		assert.Equal(t, "Acme", got.Org.Name)
		assert.Equal(t, models.OrgRoleMember, got.Org.Role)
	})

	t.Run("org member gets a personal and an org workspace", func(t *testing.T) {
		// The member and the org owner are different billing subjects, so the
		// two workspaces report different pools: the member's own monitors on
		// personal, the owner's on the org.
		got := get(t, &mockStore{
			orgsResult:           []models.Organization{{ID: "test-org-id", Name: "Acme", OwnerID: "owner-id"}},
			membershipResult:     aMembership(),
			subResult:            aSubscription("ENTERPRISE"),
			monitorCountsByOwner: map[string]int{testUserID: 2, "owner-id": 47},
		})

		require.Len(t, got.Workspaces, 2)
		assert.Equal(t, models.Workspace{
			ID: "personal", Type: models.WorkspaceTypePersonal, Plan: "ENTERPRISE", MonitorsUsed: 2, MaxMonitors: 200,
		}, got.Workspaces[0])
		assert.Equal(t, models.Workspace{
			ID: "test-org-id", Type: models.WorkspaceTypeOrg, Name: "Acme", Role: models.OrgRoleMember, Plan: "ENTERPRISE",
			MonitorsUsed: 47, MaxMonitors: 200,
		}, got.Workspaces[1])
	})

	t.Run("an org owner's two workspaces share one pooled count", func(t *testing.T) {
		owner := aMembership()
		owner.Role = models.OrgRoleOwner
		store := &mockStore{
			orgsResult:       []models.Organization{{ID: "test-org-id", Name: "Acme", OwnerID: testUserID}},
			membershipResult: owner,
			subResult:        aSubscription("ENTERPRISE"),
			monitorCount:     150,
		}
		got := get(t, store)

		require.Len(t, got.Workspaces, 2)
		// Personal monitors and org monitors draw on the same 200, so both
		// workspaces report the same usage — that is the whole point.
		assert.Equal(t, 150, got.Workspaces[0].MonitorsUsed)
		assert.Equal(t, 150, got.Workspaces[1].MonitorsUsed)
		assert.Equal(t, 200, got.Workspaces[0].MaxMonitors)
		assert.Equal(t, 200, got.Workspaces[1].MaxMonitors)
		// And the org side is not a second query against the org id.
		assert.Equal(t, testUserID, store.lastCountOwnerID)
	})

	t.Run("a free account reports the free cap", func(t *testing.T) {
		got := get(t, &mockStore{monitorCount: 3})

		require.Len(t, got.Workspaces, 1)
		assert.Equal(t, 3, got.Workspaces[0].MonitorsUsed)
		assert.Equal(t, 5, got.Workspaces[0].MaxMonitors)
	})

	t.Run("email lookup failure returns 500", func(t *testing.T) {
		router, h := newTestRouter(&mockStore{})
		h.UserEmailLookup = func(string) (string, error) { return "", errors.New("supertokens down") }
		router.GET("/v1/me", h.GetMe)

		w := doRequest(router, "GET", "/v1/me", "")

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})

	t.Run("org lookup failure returns 500", func(t *testing.T) {
		router, h := newTestRouter(&mockStore{orgsErr: errors.New("db down")})
		h.UserEmailLookup = func(string) (string, error) { return "user@example.com", nil }
		router.GET("/v1/me", h.GetMe)

		w := doRequest(router, "GET", "/v1/me", "")

		assert.Equal(t, http.StatusInternalServerError, w.Code)
	})
}
