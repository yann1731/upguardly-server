package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/supertokens/supertokens-golang/recipe/session"
	"github.com/supertokens/supertokens-golang/recipe/session/sessmodels"
	"github.com/supertokens/supertokens-golang/supertokens"
)

const UserIDKey = "userID"

func SuperTokensMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		supertokens.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c.Request = r
			c.Next()
		})).ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}

func VerifySession(options *sessmodels.VerifySessionOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		session.VerifySession(options, func(w http.ResponseWriter, r *http.Request) {
			sessionContainer := session.GetSessionFromRequestContext(r.Context())
			if sessionContainer != nil {
				c.Set(UserIDKey, sessionContainer.GetUserID())
			}
			c.Request = r
			c.Next()
		}).ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}

func GetUserID(c *gin.Context) string {
	if userID, exists := c.Get(UserIDKey); exists {
		return userID.(string)
	}
	return ""
}
