package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestClient(t *testing.T) (*Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	return &Client{rdb: redis.NewClient(&redis.Options{Addr: mr.Addr()})}, mr
}

// A rate limit of "N per window" has to actually reset after the window, even
// while requests keep arriving. It did not: every increment renewed the key's
// expiry, so under sustained traffic the window never elapsed and the limit
// became a cumulative cap that only lifted after the caller fell silent for a
// full window. A parallel browser suite hitting the login endpoint from one IP
// therefore started failing every login part-way through the run, while the
// same suite run serially passed.
func TestIncrFixedWindowDoesNotExtendTheWindow(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	const window = time.Minute

	for i := int64(1); i <= 3; i++ {
		count, err := c.IncrFixedWindow(ctx, "rl:login", window)
		if err != nil {
			t.Fatalf("IncrFixedWindow: %v", err)
		}
		if count != i {
			t.Fatalf("count after %d increments = %d, want %d", i, count, i)
		}
		// Traffic keeps arriving within the window.
		mr.FastForward(20 * time.Second)
	}

	// 60s have now elapsed since the first increment, so the window is over
	// even though requests never stopped.
	if got, err := c.IncrFixedWindow(ctx, "rl:login", window); err != nil || got != 1 {
		t.Fatalf("count in the next window = %d (err %v), want 1", got, err)
	}
}

func TestIncrFixedWindowExpiresAfterTheWindow(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()

	if _, err := c.IncrFixedWindow(ctx, "rl:login", time.Minute); err != nil {
		t.Fatalf("IncrFixedWindow: %v", err)
	}
	mr.FastForward(61 * time.Second)
	if mr.Exists("rl:login") {
		t.Fatal("counter still present after its window elapsed")
	}
}

// A key left behind without an expiry (an earlier deployment, a manual SET)
// would otherwise count up forever and lock its bucket out permanently.
func TestIncrFixedWindowRepairsAKeyWithNoExpiry(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()

	if err := mr.Set("rl:login", "7"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	count, err := c.IncrFixedWindow(ctx, "rl:login", time.Minute)
	if err != nil {
		t.Fatalf("IncrFixedWindow: %v", err)
	}
	if count != 8 {
		t.Fatalf("count = %d, want 8", count)
	}
	if ttl := mr.TTL("rl:login"); ttl != time.Minute {
		t.Fatalf("ttl = %v, want %v", ttl, time.Minute)
	}
}

// Incr keeps its sliding-window behaviour, which account lockout relies on:
// an attacker who paces failures just under the window must not get a free
// reset of the failure count.
func TestIncrSlidesTheWindowForLockoutCounters(t *testing.T) {
	c, mr := newTestClient(t)
	ctx := context.Background()
	const window = 15 * time.Minute

	for i := int64(1); i <= 3; i++ {
		count, err := c.Incr(ctx, "lockout:failures", window)
		if err != nil {
			t.Fatalf("Incr: %v", err)
		}
		if count != i {
			t.Fatalf("count after %d increments = %d, want %d", i, count, i)
		}
		mr.FastForward(14 * time.Minute)
	}
	// 42 minutes have passed — far longer than the window — yet the count
	// survives, because each failure renewed it.
	if count, err := c.Incr(ctx, "lockout:failures", window); err != nil || count != 4 {
		t.Fatalf("count = %d (err %v), want 4", count, err)
	}
}
