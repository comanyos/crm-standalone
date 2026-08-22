// Package cache provides a Redis-backed cache for sessions, flow state, and rate limits.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client wraps redis.Client with typed helpers.
type Client struct {
	rdb *redis.Client
}

// Config holds Redis connection settings.
type Config struct {
	Addr     string
	Password string
	DB       int
}

func New(cfg Config) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("cache ping: %w", err)
	}
	return &Client{rdb: rdb}, nil
}

// Ping checks that the Redis connection is alive.
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// Set stores a JSON-encoded value with a TTL.
func (c *Client) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache marshal: %w", err)
	}
	return c.rdb.Set(ctx, key, b, ttl).Err()
}

// Get retrieves and JSON-decodes a value.
func (c *Client) Get(ctx context.Context, key string, dest any) error {
	b, err := c.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return fmt.Errorf("cache miss: %s", key)
	}
	if err != nil {
		return fmt.Errorf("cache get: %w", err)
	}
	return json.Unmarshal(b, dest)
}

// Del removes one or more keys.
func (c *Client) Del(ctx context.Context, keys ...string) error {
	return c.rdb.Del(ctx, keys...).Err()
}

// Incr increments a counter and pushes its expiry back to a full ttl on every
// call, so the counter only resets after ttl of complete silence.
//
// That is a *sliding* window, and it is the right shape only where continued
// activity is itself the thing being counted — account-lockout failure counts,
// where an attacker probing just under the window must not get a free reset.
// It is the wrong shape for "N requests per minute": under steady traffic the
// expiry is renewed faster than it elapses, the counter never resets, and the
// limit turns into a cumulative lifetime cap. Use IncrFixedWindow for that.
func (c *Client) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := c.rdb.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("cache incr: %w", err)
	}
	return incr.Val(), nil
}

// fixedWindowIncr increments a counter and sets its expiry only when the
// counter starts a new window, so the window elapses ttl after the first
// request in it regardless of how much traffic follows.
//
// The PTTL branch repairs a key that is somehow already present without an
// expiry: without it such a key counts up forever and locks its bucket out
// permanently, with no way back short of deleting the key by hand.
//
// A script rather than EXPIRE ... NX so this does not require Redis 7.
var fixedWindowIncr = redis.NewScript(`
	local count = redis.call('INCR', KEYS[1])
	if count == 1 or redis.call('PTTL', KEYS[1]) < 0 then
		redis.call('PEXPIRE', KEYS[1], ARGV[1])
	end
	return count
`)

// IncrFixedWindow increments a counter within a fixed window of length ttl,
// returning the count so far in that window. The window starts at the first
// increment and ends ttl later; increments in between do not extend it.
//
// This is the counter a request rate limit needs: a caller told to retry after
// Window seconds can actually succeed then, which is not true of a window that
// each rejected request pushes further away.
func (c *Client) IncrFixedWindow(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	count, err := fixedWindowIncr.Run(ctx, c.rdb, []string{key}, ttl.Milliseconds()).Int64()
	if err != nil {
		return 0, fmt.Errorf("cache incr fixed window: %w", err)
	}
	return count, nil
}

// Exists checks if a key exists.
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	n, err := c.rdb.Exists(ctx, key).Result()
	return n > 0, err
}

// MarkTOTPUsed atomically records that a (identityID, period) TOTP code was consumed.
// Returns true if this is the FIRST use (code is fresh); false if it was already used (replay).
// Keys expire after 95 seconds — two full 30-second windows plus buffer.
func (c *Client) MarkTOTPUsed(ctx context.Context, identityID fmt.Stringer, period int64) (bool, error) {
	key := fmt.Sprintf("totp:used:%s:%d", identityID, period)
	return c.rdb.SetNX(ctx, key, 1, 95*time.Second).Result()
}

// SessionKey returns a canonical Redis key for a session.
func SessionKey(sessionID string) string { return "session:" + sessionID }

// FlowKey returns a canonical Redis key for a flow.
func FlowKey(flowID string) string { return "flow:" + flowID }

// RateLimitKey returns a canonical key for rate limiting.
func RateLimitKey(orgID, ip, action string) string {
	return fmt.Sprintf("rl:%s:%s:%s", orgID, action, ip)
}
