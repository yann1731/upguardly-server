package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *Handlers) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
	})
}
