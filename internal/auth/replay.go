package auth

import (
	"fmt"
	"sync"
	"time"
)

// ReplayWindow is the allowed clock skew between client and server for envelope
// timestamps. Requests outside [now-ReplayWindow, now+ReplayWindow] are rejected
// before signature verification even runs, so an attacker cannot bank an old
// signed request and replay it far in the future.
//
// Five minutes follows the NTP synchronization guidance used by common replay
// defenses (SSO SAML, AWS SigV4). Shorter windows break on WAN latency + clock
// drift; longer windows expand the replay horizon for little operational gain.
const ReplayWindow = 5 * time.Minute

// ErrReplay is returned when a nonce has already been observed within the
// active window. Callers should translate this to a protocol-level "already
// delivered" error to avoid leaking whether a given nonce existed.
var ErrReplay = fmt.Errorf("auth: nonce already seen within replay window")

// ErrStaleTimestamp is returned when an envelope's timestamp falls outside
// the ReplayWindow. Distinct from ErrReplay so operators can tell clock-skew
// problems from actual replay attempts.
var ErrStaleTimestamp = fmt.Errorf("auth: envelope timestamp outside replay window")

// NonceCache tracks nonces seen within the active replay window so the receiver
// can reject duplicates. The in-memory implementation is authoritative for a
// single server process; multi-instance deployments should layer a shared store
// on top (future work — see THREAT_MODEL.md B2 / nonce persistence).
type NonceCache interface {
	// Observe records that the nonce has been seen. Returns ErrReplay if the
	// nonce was already recorded (and is still within the retention window).
	// Thread-safe across goroutines.
	Observe(nonce string, now time.Time) error
}

// MemoryNonceCache is a bounded, TTL-expiring in-memory NonceCache. Suitable
// for single-instance deployments and for tests. Capacity bounds memory under
// adversarial load: once len(seen) == capacity, the oldest entry is evicted
// regardless of its TTL. This means a high-QPS attacker could push legitimate
// recent nonces out and replay them — callers that need durable guarantees
// should choose capacity >> peak_qps * ReplayWindow, or back this with a DB.
type MemoryNonceCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	capacity int
	seen     map[string]time.Time
	// order tracks insertion order for O(1) oldest-eviction when capacity is hit.
	// Using a slice (not a list) because evictions are rare in normal use and
	// the sweep path already walks everything.
	order []string
}

// NewMemoryNonceCache returns an in-memory replay cache. ttl is how long a
// nonce remains "seen"; capacity is the hard upper bound on tracked nonces.
// Pass ttl=0 to use the default 2*ReplayWindow (covers the worst-case drift
// window symmetrically).
func NewMemoryNonceCache(ttl time.Duration, capacity int) *MemoryNonceCache {
	if ttl <= 0 {
		ttl = 2 * ReplayWindow
	}
	if capacity <= 0 {
		capacity = 100_000
	}
	return &MemoryNonceCache{
		ttl:      ttl,
		capacity: capacity,
		seen:     make(map[string]time.Time, capacity),
	}
}

// Observe records the nonce. See NonceCache.Observe.
func (m *MemoryNonceCache) Observe(nonce string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sweepLocked(now)

	if _, ok := m.seen[nonce]; ok {
		return ErrReplay
	}
	if len(m.seen) >= m.capacity {
		// Evict the oldest entry outright. This is a safety valve for adversarial
		// flooding — production deployments should size capacity well above
		// expected working set or back with a durable store.
		if len(m.order) > 0 {
			oldest := m.order[0]
			m.order = m.order[1:]
			delete(m.seen, oldest)
		}
	}
	m.seen[nonce] = now.Add(m.ttl)
	m.order = append(m.order, nonce)
	return nil
}

// sweepLocked drops expired entries from the head of the order list. Called
// lazily from Observe so there's no background goroutine to manage.
func (m *MemoryNonceCache) sweepLocked(now time.Time) {
	cut := 0
	for ; cut < len(m.order); cut++ {
		exp, ok := m.seen[m.order[cut]]
		if !ok {
			continue // already removed via capacity eviction
		}
		if exp.After(now) {
			break
		}
		delete(m.seen, m.order[cut])
	}
	if cut > 0 {
		m.order = m.order[cut:]
	}
}

// ValidateTimestamp returns ErrStaleTimestamp when |now - tsUnix| > ReplayWindow.
// Exported so HTTP middleware can run it before any cryptographic work.
func ValidateTimestamp(tsUnix int64, now time.Time) error {
	diff := now.Unix() - tsUnix
	if diff < 0 {
		diff = -diff
	}
	if time.Duration(diff)*time.Second > ReplayWindow {
		return ErrStaleTimestamp
	}
	return nil
}
