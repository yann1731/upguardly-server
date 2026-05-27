package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ipBucket tracks request count within a sliding window for a single IP.
type ipBucket struct {
	count       int
	windowStart time.Time
}

// ipRateLimiter is a simple fixed-window per-IP rate limiter backed by a
// sync.Mutex map. For production workloads with many unique IPs, consider
// replacing this with a Redis-backed limiter or golang.org/x/time/rate.
type ipRateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*ipBucket
	limit     int           // max requests per window
	window    time.Duration // window duration
	lastClean time.Time
}

func newIPRateLimiter(limit int, window time.Duration) *ipRateLimiter {
	return &ipRateLimiter{
		buckets:   make(map[string]*ipBucket),
		limit:     limit,
		window:    window,
		lastClean: time.Now(),
	}
}

func (r *ipRateLimiter) allow(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Periodically clean up stale entries to prevent unbounded memory growth.
	if time.Since(r.lastClean) > 5*time.Minute {
		now := time.Now()
		for k, b := range r.buckets {
			if now.Sub(b.windowStart) > r.window {
				delete(r.buckets, k)
			}
		}
		r.lastClean = now
	}

	now := time.Now()
	b, exists := r.buckets[ip]
	if !exists || now.Sub(b.windowStart) > r.window {
		r.buckets[ip] = &ipBucket{count: 1, windowStart: now}
		return true
	}

	if b.count >= r.limit {
		return false
	}
	b.count++
	return true
}

// Global limiters with sensible defaults.
// Adjust these via environment variable parsing if needed.
var (
	// defaultLimiter: 300 requests per minute per IP (5 req/s burst tolerance).
	defaultLimiter = newIPRateLimiter(300, time.Minute)

	// strictLimiter: 30 requests per minute for sensitive write endpoints.
	strictLimiter = newIPRateLimiter(30, time.Minute)
)

func clientIP(c *gin.Context) string {
	// Prefer X-Forwarded-For if set by a trusted reverse proxy.
	// In production, ensure only trusted proxies set this header.
	if ip := c.GetHeader("X-Forwarded-For"); ip != "" {
		return ip
	}
	return c.ClientIP()
}

// RateLimit is the default rate limiting middleware (300 req/min per IP).
func RateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := clientIP(c)
		if !defaultLimiter.allow(ip) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Too many requests — please slow down",
			})
			return
		}
		c.Next()
	}
}

// StrictRateLimit is a tighter rate limiter (30 req/min per IP) for
// mutation endpoints such as monitor creation, invitations, etc.
func StrictRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := clientIP(c)
		if !strictLimiter.allow(ip) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Too many requests — please slow down",
			})
			return
		}
		c.Next()
	}
}
