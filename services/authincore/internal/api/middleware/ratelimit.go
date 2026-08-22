package middleware

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// RateLimiter is the backend used by rate-limit middleware.
// The cache.Client already satisfies this — pass it directly.
//
// IncrFixedWindow, not a sliding-window counter: a limiter whose window each
// rejected request pushes further away stops being "N per window" and becomes
// a cumulative cap that only lifts once the caller falls completely silent —
// which also makes the Retry-After below untrue.
type RateLimiter interface {
	IncrFixedWindow(ctx context.Context, key string, ttl time.Duration) (int64, error)
	Exists(ctx context.Context, key string) (bool, error)
}

// RateLimitConfig defines limits for a single route class.
type RateLimitConfig struct {
	// MaxRequests allowed in Window.
	MaxRequests int64
	Window      time.Duration
	// KeyFunc derives the bucket key from the request.
	// Returning "" disables limiting for this request.
	KeyFunc func(r *http.Request) string
	// FailClosed rejects the request when the shared limiter is unavailable.
	// Enable this for credential, recovery, and token endpoints.
	FailClosed bool
}

// RateLimit returns middleware that enforces the given config.
// Returns 429 with Retry-After when the limit is exceeded.
func RateLimit(rl RateLimiter, cfg RateLimitConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := cfg.KeyFunc(r)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			count, err := rl.IncrFixedWindow(r.Context(), "rl:"+key, cfg.Window)
			if err != nil {
				if cfg.FailClosed {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":"rate_limiter_unavailable","message":"authentication temporarily unavailable"}`))
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			if count > cfg.MaxRequests {
				retryAfter := int(cfg.Window.Seconds())
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", cfg.MaxRequests))
				w.Header().Set("X-RateLimit-Remaining", "0")
				http.Error(w, `{"error":"rate_limit_exceeded","message":"too many requests"}`, http.StatusTooManyRequests)
				return
			}

			remaining := cfg.MaxRequests - count
			w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", cfg.MaxRequests))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
			next.ServeHTTP(w, r)
		})
	}
}

// IPKeyFunc extracts the real client IP, respecting X-Forwarded-For from trusted proxies.
func IPKeyFunc(action string, trustedProxies ...*net.IPNet) func(r *http.Request) string {
	return func(r *http.Request) string {
		ip := realIP(r, trustedProxies)
		return fmt.Sprintf("%s:%s", action, ip)
	}
}

// OrgIPKeyFunc combines org ID + IP for per-org limiting.
func OrgIPKeyFunc(action string, trustedProxies ...*net.IPNet) func(r *http.Request) string {
	return func(r *http.Request) string {
		ip := realIP(r, trustedProxies)
		orgID := r.Header.Get("X-Org-ID")
		if orgID == "" {
			orgID = "default"
		}
		return fmt.Sprintf("%s:%s:%s", action, orgID, ip)
	}
}

func realIP(r *http.Request, trustedProxies []*net.IPNet) string {
	peer := remoteIP(r.RemoteAddr)
	if isTrustedProxy(peer, trustedProxies) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if candidate := net.ParseIP(strings.TrimSpace(parts[0])); candidate != nil {
				return candidate.String()
			}
		}
		if candidate := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); candidate != nil {
			return candidate.String()
		}
	}
	if peer != nil {
		return peer.String()
	}
	return r.RemoteAddr
}

func remoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(remoteAddr)
}

func isTrustedProxy(peer net.IP, trustedProxies []*net.IPNet) bool {
	if peer == nil {
		return false
	}
	for _, network := range trustedProxies {
		if network != nil && network.Contains(peer) {
			return true
		}
	}
	return false

}

// ParseTrustedProxyCIDRs validates a comma-separated proxy CIDR allowlist.
func ParseTrustedProxyCIDRs(raw string) ([]*net.IPNet, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var networks []*net.IPNet
	for _, item := range strings.Split(raw, ",") {
		_, network, err := net.ParseCIDR(strings.TrimSpace(item))
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", item, err)
		}
		networks = append(networks, network)
	}
	return networks, nil
}
