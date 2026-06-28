package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestRouter returns a router whose single route is guarded by the default
// rate limiter. Each call resets the limiters (fresh in-memory buckets) so tests
// don't bleed into each other via the package globals.
func newTestRouter(limit int) *gin.Engine {
	SetRedisClient(nil) // force the in-memory path
	InitRateLimiters(limit, limit, time.Minute)

	r := gin.New()
	r.Use(RateLimit())
	r.GET("/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })
	return r
}

func doGet(r *gin.Engine) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	req.RemoteAddr = "203.0.113.7:5555" // stable client IP key
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRateLimitBlocksAfterLimit(t *testing.T) {
	r := newTestRouter(3)

	for i := 1; i <= 3; i++ {
		if w := doGet(r); w.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i, w.Code)
		}
	}

	w := doGet(r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("4th request: got %d, want 429", w.Code)
	}
}

func TestRateLimitHeaders(t *testing.T) {
	r := newTestRouter(2)

	w1 := doGet(r)
	if got := w1.Header().Get("RateLimit-Limit"); got != "2" {
		t.Errorf("RateLimit-Limit: got %q, want \"2\"", got)
	}
	if got := w1.Header().Get("RateLimit-Remaining"); got != "1" {
		t.Errorf("RateLimit-Remaining after 1st: got %q, want \"1\"", got)
	}
	if got := w1.Header().Get("RateLimit-Reset"); got == "" {
		t.Error("RateLimit-Reset should be set")
	}
	if got := w1.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After should be absent on an allowed request, got %q", got)
	}

	w2 := doGet(r)
	if got := w2.Header().Get("RateLimit-Remaining"); got != "0" {
		t.Errorf("RateLimit-Remaining after 2nd: got %q, want \"0\"", got)
	}

	w3 := doGet(r) // over budget
	if w3.Code != http.StatusTooManyRequests {
		t.Fatalf("3rd request: got %d, want 429", w3.Code)
	}
	if got := w3.Header().Get("Retry-After"); got == "" {
		t.Error("Retry-After should be set on a 429")
	}
	if got := w3.Header().Get("RateLimit-Remaining"); got != "0" {
		t.Errorf("RateLimit-Remaining on 429: got %q, want \"0\"", got)
	}
}

func TestInitRateLimitersChangesThreshold(t *testing.T) {
	r := newTestRouter(1)

	if w := doGet(r); w.Code != http.StatusOK {
		t.Fatalf("1st request: got %d, want 200", w.Code)
	}
	if w := doGet(r); w.Code != http.StatusTooManyRequests {
		t.Fatalf("2nd request with limit=1: got %d, want 429", w.Code)
	}
}
