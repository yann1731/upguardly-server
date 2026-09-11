package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/models"
)

// membershipStore stubs the one Store method ResolveWorkspace calls; embedding
// the interface leaves every other method nil (a call would panic, flagging
// an unexpected dependency).
type membershipStore struct {
	models.Store
	member *models.OrganizationMember
	err    error
}

func (s membershipStore) GetMembership(_ context.Context, _, _ string) (*models.OrganizationMember, error) {
	return s.member, s.err
}

func runWorkspace(t *testing.T, store models.Store, header string) (*httptest.ResponseRecorder, Workspace) {
	t.Helper()
	var got Workspace
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set(SessionKey, "user-1"); c.Next() })
	r.Use(ResolveWorkspace(store))
	r.GET("/x", func(c *gin.Context) {
		got = GetWorkspace(c)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if header != "" {
		req.Header.Set(WorkspaceHeader, header)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w, got
}

func TestResolveWorkspace(t *testing.T) {
	t.Run("no header is the implicit personal workspace", func(t *testing.T) {
		w, ws := runWorkspace(t, membershipStore{}, "")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if ws.IsOrg() || ws.Explicit {
			t.Errorf("workspace = %+v, want implicit personal", ws)
		}
	})

	t.Run("personal header is the explicit personal workspace", func(t *testing.T) {
		_, ws := runWorkspace(t, membershipStore{}, PersonalWorkspaceID)
		if ws.IsOrg() || !ws.Explicit {
			t.Errorf("workspace = %+v, want explicit personal", ws)
		}
	})

	t.Run("member selects the org workspace with their role", func(t *testing.T) {
		store := membershipStore{member: &models.OrganizationMember{Role: models.OrgRoleViewer}}
		w, ws := runWorkspace(t, store, "org-1")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if ws.OrgID != "org-1" || ws.Role != models.OrgRoleViewer || !ws.Explicit {
			t.Errorf("workspace = %+v, want org-1 as VIEWER", ws)
		}
	})

	t.Run("non-member is refused", func(t *testing.T) {
		w, _ := runWorkspace(t, membershipStore{err: models.ErrNotFound}, "org-1")
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", w.Code)
		}
	})
}

func TestGetWorkspaceWithoutMiddleware(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if ws := GetWorkspace(c); ws.IsOrg() || ws.Explicit {
		t.Errorf("workspace = %+v, want implicit personal", ws)
	}
}
