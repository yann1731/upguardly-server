package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/supertokens/supertokens-golang/recipe/session"
)

// SessionKey is the Gin context key for the authenticated user ID.
const SessionKey = "userId"

// AuthRequired verifies the SuperTokens session and sets userId in the Gin context.
func AuthRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		session.VerifySession(nil, func(rw http.ResponseWriter, r *http.Request) {
			sess := session.GetSessionFromRequestContext(r.Context())
			c.Set(SessionKey, sess.GetUserID())
			c.Request = c.Request.WithContext(r.Context())
			c.Next()
		})(c.Writer, c.Request)
		c.Abort()
	}
}

// GetUserID retrieves the authenticated user ID from the Gin context.
func GetUserID(c *gin.Context) (string, bool) {
	val, exists := c.Get(SessionKey)
	if !exists {
		return "", false
	}
	id, ok := val.(string)
	return id, ok
}
