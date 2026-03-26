package payments

import (
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
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		windows: make(map[string][]time.Time),
		limit:   limit,
		window:  window,
	}
	// Periodic cleanup of stale keys to prevent memory leak
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			rl.cleanup()
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
