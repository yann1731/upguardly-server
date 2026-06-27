// Package redisclient provides a small constructor for a shared Redis client.
// The same client is reused across features that need Redis (distributed rate
// limiting today, response caching later), so connection pooling is shared.
package redisclient

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// New builds a Redis client from a standard connection URL
// (redis://[:password@]host:port/db). When url is empty it returns (nil, nil)
// so callers can treat Redis as optional and fall back to a local default.
//
// New pings the server once to surface misconfiguration early; a failed ping is
// returned as an error but the caller decides whether that is fatal.
func New(url string) (*redis.Client, error) {
	if url == "" {
		return nil, nil
	}

	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}

	client := redis.NewClient(opt)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return client, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}
