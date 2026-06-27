package middleware

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// redisClient is an optional shared Redis client used for distributed rate
// limiting across multiple API replicas. When nil, limiters fall back to a
// per-process in-memory map (correct only for single-instance deployments).
// Set once at startup via SetRedisClient before the router serves traffic.
var redisClient *redis.Client

// SetRedisClient installs the shared Redis client used by the rate limiters.
// Passing nil (the default) keeps the in-memory fallback.
func SetRedisClient(c *redis.Client) {
	redisClient = c
}

// fixedWindowScript atomically increments a per-IP counter and, on the first
// increment of a window, sets its expiry. Doing both in one script avoids a
// window whose key never expires if the process dies between INCR and EXPIRE.
// Returns the current count.
var fixedWindowScript = redis.NewScript(`
local c = redis.call('INCR', KEYS[1])
if c == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return c
`)

// ipBucket tracks request count within a fixed window for a single IP
// (in-memory fallback only).
type ipBucket struct {
	count       int
	windowStart time.Time
}

// rateLimiter enforces a fixed-window per-IP limit. It uses Redis when a shared
// client is configured (so the limit is global across replicas) and otherwise
// falls back to an in-process map.
type rateLimiter struct {
	name   string        // distinguishes limiters sharing the same IP key space
	limit  int           // max requests per window
	window time.Duration // window duration

	mu        sync.Mutex
	buckets   map[string]*ipBucket
	lastClean time.Time
}

func newRateLimiter(name string, limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		name:      name,
		limit:     limit,
		window:    window,
		buckets:   make(map[string]*ipBucket),
		lastClean: time.Now(),
	}
}

// allow reports whether a request from ip is permitted under the limit.
func (r *rateLimiter) allow(ip string) bool {
	if redisClient != nil {
		allowed, err := r.allowRedis(ip)
		if err != nil {
			// Fail open: a Redis outage must not take the API down. Log so the
			// degradation is visible, then permit the request.
			log.Printf("[WARN] rate limiter: redis error, failing open: %v", err)
			return true
		}
		return allowed
	}
	return r.allowMemory(ip)
}

func (r *rateLimiter) allowRedis(ip string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	key := fmt.Sprintf("ratelimit:%s:%s", r.name, ip)
	count, err := fixedWindowScript.Run(ctx, redisClient, []string{key}, r.window.Milliseconds()).Int64()
	if err != nil {
		return false, err
	}
	return count <= int64(r.limit), nil
}

func (r *rateLimiter) allowMemory(ip string) bool {
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
var (
	// defaultLimiter: 300 requests per minute per IP (5 req/s burst tolerance).
	defaultLimiter = newRateLimiter("default", 300, time.Minute)

	// strictLimiter: 30 requests per minute for sensitive write endpoints.
	strictLimiter = newRateLimiter("strict", 30, time.Minute)
)

// clientIP returns the request's client IP. It relies on gin.Context.ClientIP,
// which honours the engine's trusted-proxy configuration (set via
// SetTrustedProxies in the router). This prevents clients from bypassing the
// limiter by spoofing X-Forwarded-For: only entries added by trusted proxies
// are believed. The result is normalised so "ip:port" and bracketed IPv6
// forms collapse to a stable key.
func clientIP(c *gin.Context) string {
	ip := c.ClientIP()
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return strings.TrimSpace(ip)
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
