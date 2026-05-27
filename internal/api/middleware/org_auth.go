package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"upguardly-backend/internal/models"
)

const OrgIDKey = "orgId"
const OrgRoleKey = "orgRole"

// RequireOrgRole returns a middleware that enforces org membership with at least minRole.
// It reads the org ID from the ":id" path param and sets orgId + orgRole in context on success.
// Must be used after AuthRequired so that userId is already set.
func RequireOrgRole(store models.Store, minRole models.OrgRole) gin.HandlerFunc {
	return func(c *gin.Context) {
		userId, ok := GetUserID(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}

		orgId := c.Param("id")
		if orgId == "" {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Missing organization ID"})
			return
		}

		membership, err := store.GetMembership(c.Request.Context(), orgId, userId)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Not a member of this organization"})
			return
		}

		if !models.RoleAtLeast(membership.Role, minRole) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Insufficient permissions"})
			return
		}

		c.Set(OrgIDKey, orgId)
		c.Set(OrgRoleKey, membership.Role)
		c.Next()
	}
}

// GetOrgID retrieves the org ID set by RequireOrgRole.
func GetOrgID(c *gin.Context) (string, bool) {
	val, exists := c.Get(OrgIDKey)
	if !exists {
		return "", false
	}
	id, ok := val.(string)
	return id, ok
}

// GetOrgRole retrieves the caller's org role set by RequireOrgRole.
func GetOrgRole(c *gin.Context) (models.OrgRole, bool) {
	val, exists := c.Get(OrgRoleKey)
	if !exists {
		return "", false
	}
	role, ok := val.(models.OrgRole)
	return role, ok
}
