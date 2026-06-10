package payments

import (
	"sync"
	"time"
)

// SandboxQuota throttles unauthenticated sandbox calls across the paid
// analyze actions (run, propose_compromise, expert_panel, follow_up).
//
// Semantics: each call against an identity (typically client IP) increments
// a counter. When the counter exceeds Limit within Window, subsequent calls
// are denied — the handler should respond with a 402 + payment challenges
// so the agent can either fund credits or pay via MPP.
//
// Limitations (deliberate, deferred):
//   - IP-based identity is rotatable (VPN). This is a soft throttle, not a
//     hard abuse-resistance layer; that comes from the credit/MPP funding
//     requirement at higher tiers.
//   - In-memory state means multi-machine fly deployments effectively give
//     N×machines/day. Acceptable for a soft throttle; if abuse signal
//     emerges, back this with Postgres or Redis.
//   - Rolling 24h window from first call (not calendar day). Simpler,
//     avoids midnight burst patterns.
type SandboxQuota struct {
	mu     sync.Mutex
	counts map[string]quotaEntry
	limit  int
	window time.Duration
}

type quotaEntry struct {
	count   int
	resetAt time.Time
}

// NewSandboxQuota returns a fresh quota tracker. Limit is the maximum
// number of calls per identity within Window (e.g. NewSandboxQuota(20,
// 24*time.Hour) → 20 calls per IP per 24h).
func NewSandboxQuota(limit int, window time.Duration) *SandboxQuota {
	return &SandboxQuota{
		counts: make(map[string]quotaEntry),
		limit:  limit,
		window: window,
	}
}

// Allow checks if the identity has remaining quota, and if so, increments
// the counter. Returns (true, remaining) when the call is permitted,
// (false, 0) when denied. Empty identity is permitted without tracking —
// callers should pass a non-empty identity (typically ClientIP); falling
// back to "permit without tracking" is safer than a stop-the-world cache
// poison from an unexpected empty key.
func (q *SandboxQuota) Allow(identity string) (allowed bool, remaining int) {
	if identity == "" {
		return true, q.limit
	}
	q.mu.Lock()
	defer q.mu.Unlock()

	// Prune expired entries opportunistically — bounds map growth without
	// a background goroutine.
	now := time.Now()
	for k, e := range q.counts {
		if now.After(e.resetAt) {
			delete(q.counts, k)
		}
	}

	entry, ok := q.counts[identity]
	if !ok || now.After(entry.resetAt) {
		// Fresh window for this identity.
		q.counts[identity] = quotaEntry{count: 1, resetAt: now.Add(q.window)}
		return true, q.limit - 1
	}

	if entry.count >= q.limit {
		return false, 0
	}

	entry.count++
	q.counts[identity] = entry
	return true, q.limit - entry.count
}

// Refund decrements the counter for an identity by 1, used when a call was
// counted but the action ultimately couldn't run (early validation error
// AFTER the quota check). Never goes negative. No-op on empty identity or
// unknown key.
func (q *SandboxQuota) Refund(identity string) {
	if identity == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	entry, ok := q.counts[identity]
	if !ok {
		return
	}
	if entry.count > 0 {
		entry.count--
		q.counts[identity] = entry
	}
}
