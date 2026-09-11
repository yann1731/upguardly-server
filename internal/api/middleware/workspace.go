package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/models"
)

// WorkspaceHeader selects the workspace a collection request (list/create
// monitors, account integrations) operates in: PersonalWorkspaceID or an org
// id. Per-resource routes (/monitors/:id/...) ignore it and authorize by the
// resource itself, so deep links keep working whatever workspace is selected.
const WorkspaceHeader = "X-Workspace-Id"

// PersonalWorkspaceID is the header value for the caller's own workspace.
const PersonalWorkspaceID = "personal"

const workspaceKey = "workspace"

// Workspace is the resolved workspace of a request. OrgID is empty for the
// personal workspace; Role is the caller's membership role in the org
// workspace. Explicit reports whether the client sent the header at all, so
// handlers can keep honoring legacy request fields when it is absent.
type Workspace struct {
	OrgID    string
	Role     models.OrgRole
	Explicit bool
}

// IsOrg reports whether this is an org workspace.
func (w Workspace) IsOrg() bool { return w.OrgID != "" }

// ResolveWorkspace reads WorkspaceHeader and verifies the caller belongs to the
// selected org. A missing or "personal" header selects the personal workspace;
// an org the caller isn't a member of is refused with 403 workspace_forbidden.
// Must be used after AuthRequired so that userId is already set.
func ResolveWorkspace(store models.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		userId, ok := GetUserID(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}

		raw := strings.TrimSpace(c.GetHeader(WorkspaceHeader))
		if raw == "" || raw == PersonalWorkspaceID {
			c.Set(workspaceKey, Workspace{Explicit: raw != ""})
			c.Next()
			return
		}

		membership, err := store.GetMembership(c.Request.Context(), raw, userId)
		if err != nil || membership == nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "You are not a member of this workspace",
				"code":  "workspace_forbidden",
			})
			return
		}

		c.Set(workspaceKey, Workspace{OrgID: raw, Role: membership.Role, Explicit: true})
		c.Next()
	}
}

// GetWorkspace returns the workspace set by ResolveWorkspace, or the implicit
// personal workspace when the middleware didn't run.
func GetWorkspace(c *gin.Context) Workspace {
	if val, exists := c.Get(workspaceKey); exists {
		if ws, ok := val.(Workspace); ok {
			return ws
		}
	}
	return Workspace{}
}
