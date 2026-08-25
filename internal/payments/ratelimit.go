package payments

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter implements a per-key sliding window rate limiter.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string][]time.Time
	limit   int
	window  time.Duration
}

// NewRateLimiter creates a rate limiter allowing limit requests per window per key.
// The cleanup goroutine stops when ctx is cancelled.
func NewRateLimiter(ctx context.Context, limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		windows: make(map[string][]time.Time),
		limit:   limit,
		window:  window,
	}
	// Periodic cleanup of stale keys to prevent memory leak
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rl.cleanup()
			}
		}
	}()
	return rl
}

// Allow returns true if the key has not exceeded its rate limit.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-r.window)

	// Prune expired entries
	times := r.windows[key]
	start := 0
	for start < len(times) && times[start].Before(cutoff) {
		start++
	}
	times = times[start:]

	if len(times) >= r.limit {
		r.windows[key] = times
		return false
	}

	r.windows[key] = append(times, now)
	return true
}

// cleanup removes keys with no recent activity.
func (r *RateLimiter) cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-r.window)
	for key, times := range r.windows {
		if len(times) == 0 || times[len(times)-1].Before(cutoff) {
			delete(r.windows, key)
		}
	}
}

// SetRateLimitHeaders sets the IETF-draft RateLimit-* response headers
// (RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset) on every response
// from a rate-limited endpoint, plus Retry-After (RFC 9110 §10.2.3) when the
// request was rejected — so an agent can self-throttle from the headers
// alone instead of trial-and-error against 429s. Call once, right after the
// Allow(key) check that decided allowed.
func (r *RateLimiter) SetRateLimitHeaders(w http.ResponseWriter, key string, allowed bool) {
	remaining, resetAt := r.Status(key)
	resetSeconds := int(time.Until(resetAt).Seconds())
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	h := w.Header()
	h.Set("RateLimit-Limit", strconv.Itoa(r.limit))
	h.Set("RateLimit-Remaining", strconv.Itoa(remaining))
	h.Set("RateLimit-Reset", strconv.Itoa(resetSeconds))
	if !allowed {
		h.Set("Retry-After", strconv.Itoa(resetSeconds))
	}
}

// Status returns the requests remaining in key's current window and when
// that window resets (the moment the oldest in-window request ages out).
// Read-only — does not mutate state or count as a request. Callers use this
// to set the standard rate-limit response headers (see SetRateLimitHeaders)
// so agents can self-throttle instead of guessing.
func (r *RateLimiter) Status(key string) (remaining int, resetAt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-r.window)
	times := r.windows[key]
	start := 0
	for start < len(times) && times[start].Before(cutoff) {
		start++
	}
	times = times[start:]

	remaining = r.limit - len(times)
	if remaining < 0 {
		remaining = 0
	}
	if len(times) > 0 {
		resetAt = times[0].Add(r.window)
	} else {
		resetAt = now
	}
	return remaining, resetAt
}
