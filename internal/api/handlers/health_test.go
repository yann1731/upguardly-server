package handlers_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHealth(t *testing.T) {
	router, h := newTestRouter(&mockStore{})
	router.GET("/v1/health", h.Health)

	w := doRequest(router, "GET", "/v1/health", "")

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "ok")
}
