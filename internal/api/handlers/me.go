package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/supertokens/supertokens-golang/recipe/emailpassword"

	"upguardly-backend/internal/api/middleware"
)

// GetMe returns the authenticated user's identity. SuperTokens owns the user
// record (there is no local user table), so the email is resolved from the
// emailpassword recipe. The frontend uses this to display the account email and
// to drive the password-reset flow, neither of which the web SDK can do with the
// user ID alone.
func (h *Handlers) GetMe(c *gin.Context) {
	userId, ok := middleware.GetUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	user, err := emailpassword.GetUserByID(userId)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
		return
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"userId": user.ID,
		"email":  user.Email,
	})
}
