package middleware

import (
	"context"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"upguardly-backend/internal/metrics"
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
// It returns {count, remaining-ttl-ms} so callers can populate RateLimit-*
// headers without a second round-trip.
var fixedWindowScript = redis.NewScript(`
local c = redis.call('INCR', KEYS[1])
if c == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return {c, redis.call('PTTL', KEYS[1])}
`)

// limitResult is the outcome of a single limiter check. remaining and resetAfter
// drive the RateLimit-* / Retry-After response headers.
type limitResult struct {
	allowed    bool
	limit      int
	remaining  int
	resetAfter time.Duration
}

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

// allow reports whether a request from ip is permitted under the limit, along
// with the remaining budget and time until the window resets.
func (r *rateLimiter) allow(ip string) limitResult {
	if redisClient != nil {
		res, err := r.allowRedis(ip)
		if err != nil {
			// Fail open: a Redis outage must not take the API down. Count it so
			// the degradation is visible (the global limit is not enforced while
			// this is happening), log, then permit the request.
			metrics.RateLimitRedisErrorsTotal.Inc()
			log.Printf("[WARN] rate limiter: redis error, failing open: %v", err)
			return limitResult{allowed: true, limit: r.limit, remaining: r.limit, resetAfter: r.window}
		}
		return res
	}
	return r.allowMemory(ip)
}

func (r *rateLimiter) allowRedis(ip string) (limitResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	key := fmt.Sprintf("ratelimit:%s:%s", r.name, ip)
	vals, err := fixedWindowScript.Run(ctx, redisClient, []string{key}, r.window.Milliseconds()).Int64Slice()
	if err != nil {
		return limitResult{}, err
	}
	if len(vals) != 2 {
		return limitResult{}, fmt.Errorf("unexpected script result length %d", len(vals))
	}
	count, ttlMs := vals[0], vals[1]

	resetAfter := r.window
	if ttlMs > 0 {
		resetAfter = time.Duration(ttlMs) * time.Millisecond
	}
	return limitResult{
		allowed:    count <= int64(r.limit),
		limit:      r.limit,
		remaining:  remaining(r.limit, count),
		resetAfter: resetAfter,
	}, nil
}

func (r *rateLimiter) allowMemory(ip string) limitResult {
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
		return limitResult{allowed: true, limit: r.limit, remaining: remaining(r.limit, 1), resetAfter: r.window}
	}

	resetAfter := r.window - now.Sub(b.windowStart)
	if b.count >= r.limit {
		return limitResult{allowed: false, limit: r.limit, remaining: 0, resetAfter: resetAfter}
	}
	b.count++
	return limitResult{allowed: true, limit: r.limit, remaining: remaining(r.limit, int64(b.count)), resetAfter: resetAfter}
}

// remaining returns the non-negative budget left in the window after count
// requests.
func remaining(limit int, count int64) int {
	if r := int64(limit) - count; r > 0 {
		return int(r)
	}
	return 0
}

// Global limiters. Defaults are conservative single-box values; production
// overrides them via InitRateLimiters using the values from RATE_LIMIT_* env.
var (
	defaultLimiter = newRateLimiter("default", 300, time.Minute)
	strictLimiter  = newRateLimiter("strict", 30, time.Minute)
)

// InitRateLimiters rebuilds the global limiters with the configured budgets and
// window. Call once at startup before the router serves traffic (mirrors
// SetRedisClient). Non-positive values keep the existing default.
func InitRateLimiters(defaultPerMin, strictPerMin int, window time.Duration) {
	if window <= 0 {
		window = time.Minute
	}
	if defaultPerMin > 0 {
		defaultLimiter = newRateLimiter("default", defaultPerMin, window)
	}
	if strictPerMin > 0 {
		strictLimiter = newRateLimiter("strict", strictPerMin, window)
	}
}

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

// setRateLimitHeaders advertises the budget so clients can self-throttle. Uses
// the IETF draft RateLimit-* field names; Reset is whole seconds until the
// window rolls over.
func setRateLimitHeaders(c *gin.Context, res limitResult) {
	resetSecs := int(math.Ceil(res.resetAfter.Seconds()))
	c.Header("RateLimit-Limit", strconv.Itoa(res.limit))
	c.Header("RateLimit-Remaining", strconv.Itoa(res.remaining))
	c.Header("RateLimit-Reset", strconv.Itoa(resetSecs))
}

// enforce applies a limiter to the request: it sets the RateLimit-* headers and,
// when the budget is exhausted, adds Retry-After, records the block, and aborts
// with 429.
func enforce(c *gin.Context, l *rateLimiter) {
	res := l.allow(clientIP(c))
	setRateLimitHeaders(c, res)
	if !res.allowed {
		retry := int(math.Ceil(res.resetAfter.Seconds()))
		if retry < 1 {
			retry = 1
		}
		c.Header("Retry-After", strconv.Itoa(retry))
		metrics.RateLimitBlockedTotal.WithLabelValues(l.name).Inc()
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error": "Too many requests — please slow down",
		})
		return
	}
	c.Next()
}

// RateLimit is the default rate limiting middleware (per-IP global budget).
func RateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		enforce(c, defaultLimiter)
	}
}

// StrictRateLimit is a tighter rate limiter for mutation endpoints such as
// monitor creation, invitations, etc.
func StrictRateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		enforce(c, strictLimiter)
	}
}
