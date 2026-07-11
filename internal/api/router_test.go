package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// clientIPThrough builds an engine with the given trusted-proxy list and reports
// what gin.Context.ClientIP resolves to for a request arriving from peer with
// the given X-Forwarded-For chain. peer is the address gin sees on the socket —
// in production always Caddy, on the docker bridge.
func clientIPThrough(t *testing.T, trusted []string, peer, xff string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies(trusted); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}

	var got string
	r.GET("/", func(c *gin.Context) { got = c.ClientIP() })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = peer + ":54321"
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	r.ServeHTTP(httptest.NewRecorder(), req)
	return got
}

func TestTrustedProxiesExpandsCloudflare(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "172.16.0.0/12,cloudflare")
	got := trustedProxies()

	if len(got) != 1+len(cloudflareProxies) {
		t.Fatalf("got %d entries, want %d", len(got), 1+len(cloudflareProxies))
	}
	if got[0] != "172.16.0.0/12" {
		t.Errorf("literal entry not preserved: %q", got[0])
	}
	// 162.158.0.0/15 covers the edge IP seen in the production logs.
	var found bool
	for _, c := range got {
		if c == "162.158.0.0/15" {
			found = true
		}
	}
	if !found {
		t.Error("cloudflare token did not expand to the edge ranges")
	}
}

func TestTrustedProxiesDefaultsToPrivate(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "")
	if got := trustedProxies(); len(got) != len(privateProxies) {
		t.Errorf("unset TRUSTED_PROXIES: got %v, want the private ranges", got)
	}
}

// The bug this fixes: with only the private ranges trusted, gin stops walking
// X-Forwarded-For at Cloudflare's edge, so every user behind a given edge IP
// shares one rate-limit bucket.
func TestClientIPResolvesRealClientBehindCloudflare(t *testing.T) {
	const (
		caddy      = "172.19.0.1"      // docker bridge peer
		cfEdge     = "162.158.127.220" // from the production log
		realClient = "203.0.113.7"     // what we actually want to rate-limit on
	)
	xff := realClient + ", " + cfEdge // CF sets the client; Caddy appends the edge

	t.Run("before: stops at the cloudflare edge", func(t *testing.T) {
		if got := clientIPThrough(t, privateProxies, caddy, xff); got != cfEdge {
			t.Errorf("got %q, want the edge IP %q (documents the old behaviour)", got, cfEdge)
		}
	})

	t.Run("after: resolves the real client", func(t *testing.T) {
		trusted := append(append([]string{}, privateProxies...), cloudflareProxies...)
		if got := clientIPThrough(t, trusted, caddy, xff); got != realClient {
			t.Errorf("got %q, want the real client %q", got, realClient)
		}
	})
}

// A caller reaching the origin directly (Caddy publishes 80/443 to the
// internet) must not be able to choose their own rate-limit key. Caddy appends
// the true peer address to X-Forwarded-For, and that peer is not a trusted
// proxy, so gin's right-to-left walk stops there and the forged entries ahead
// of it are ignored. This is what gin's TrustedPlatform/CF-Connecting-IP
// shortcut would NOT protect against: it reads the header before any peer check.
func TestClientIPIgnoresSpoofedForwardedForFromDirectCaller(t *testing.T) {
	const (
		caddy    = "172.19.0.1"
		attacker = "198.51.100.99"
	)
	trusted := append(append([]string{}, privateProxies...), cloudflareProxies...)

	// The attacker forges a chain, including a Cloudflare edge IP, to look like
	// it arrived through the CDN. Caddy still appends their real address last.
	xff := "1.2.3.4, 162.158.127.220, " + attacker

	if got := clientIPThrough(t, trusted, caddy, xff); got != attacker {
		t.Errorf("spoofable client IP: got %q, want the attacker's true peer %q", got, attacker)
	}
}
