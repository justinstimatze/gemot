package principal

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file is Phase 2 of docs/remote-trust-root.md: resolving a trusted
// issuer's signing keys from a JWKS endpoint instead of pinning them in config.
// It adds exactly one new risk class over Phase 1 — the verification path may now
// cross the network — and every control in §4.6 of the design lives here:
//
//   - JWKS URLs come only from operator config, never from a credential (T4).
//   - The HTTP client refuses to connect to loopback/link-local/private
//     addresses, so a config'd URL that resolves (or redirects, or DNS-rebinds)
//     to an internal address cannot be used to reach internal services (T4).
//   - The hot path is cache-only in the common case; fetches are bounded by a
//     tight timeout, size-capped, rate-limited, and fail closed to ErrKeyLookup
//     rather than stalling the submit path (T5).
//   - The cache serves last-good keys across a transient outage and refreshes on
//     a capped TTL, which is how rotation is picked up — issuers publish the new
//     key alongside the old before cutting over, so overlapping keys verify (T6).
//
// Native gemot credentials carry no JWT `kid`, so key selection is "try every
// current Ed25519 key the issuer publishes." That is safe (a signature either
// matches one of the issuer's real keys or it does not) and keeps the wire
// format unchanged; per-`kid` selection belongs to the JWT phase (Phase 3).

const (
	// jwksFetchTimeout bounds a single JWKS fetch end-to-end. The verification
	// path must never hang on a slow or hostile endpoint, so this is short and
	// the fetch fails closed when it elapses.
	jwksFetchTimeout = 10 * time.Second

	// jwksDialTimeout bounds the TCP connect for a JWKS fetch.
	jwksDialTimeout = 5 * time.Second

	// maxJWKSBytes caps the response body. A JWKS is a handful of small keys; a
	// multi-megabyte body is either a misconfiguration or an attempt to exhaust
	// memory, and either way we refuse it.
	maxJWKSBytes = 1 << 20 // 1 MiB

	// maxJWKSKeys caps how many keys we retain from one document, bounding both
	// memory and the per-verify signature-attempt loop.
	maxJWKSKeys = 32

	// defaultJWKSCacheTTL is how long fetched keys are served before a refresh is
	// attempted. It also rate-limits fetch attempts, so a flood of bad-signature
	// credentials can never drive more than one fetch per issuer per TTL.
	defaultJWKSCacheTTL = 5 * time.Minute
)

// keySource yields the candidate Ed25519 verification keys for an issuer at
// verify time. A static source returns the pinned key; a JWKS source returns the
// issuer's currently-published keys, fetching and caching as needed.
type keySource interface {
	keys(ctx context.Context) ([][]byte, error)
}

// staticKeySource wraps a single config-pinned key (Phase 1 behavior).
type staticKeySource struct {
	key []byte
}

func (s staticKeySource) keys(context.Context) ([][]byte, error) {
	return [][]byte{s.key}, nil
}

// jwksKeySource resolves an issuer's keys from a JWKS URL, with caching, single-
// flight refresh, and fail-closed error handling.
type jwksKeySource struct {
	url    string
	client *http.Client
	ttl    time.Duration
	now    func() time.Time

	mu        sync.Mutex
	cached    [][]byte  // last successfully-fetched keys
	fetchedAt time.Time // when cached was last refreshed
	lastTry   time.Time // when a fetch was last attempted (success or failure)
}

// keys returns the issuer's current signing keys, refreshing from the JWKS
// endpoint when the cache is stale or empty.
//
// The mutex is held across the fetch on purpose: it single-flights concurrent
// verifies for the same issuer behind one request rather than stampeding the
// endpoint. Fetches are rate-limited to at most once per TTL so a stream of
// unverifiable credentials cannot amplify into a stream of outbound fetches.
func (s *jwksKeySource) keys(ctx context.Context) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if len(s.cached) > 0 && now.Sub(s.fetchedAt) < s.ttl {
		return s.cached, nil // fresh
	}
	// Stale or never fetched. Back off if we tried recently, to bound outbound
	// fetch rate regardless of inbound request rate.
	if !s.lastTry.IsZero() && now.Sub(s.lastTry) < s.ttl {
		if len(s.cached) > 0 {
			return s.cached, nil // serve last-good rather than hammer the endpoint
		}
		return nil, fmt.Errorf("%w: JWKS for %s unavailable (recent fetch failed, backing off)", ErrKeyLookup, s.url)
	}

	s.lastTry = now
	fetched, err := fetchJWKS(ctx, s.client, s.url)
	if err != nil {
		if len(s.cached) > 0 {
			// Availability: a transient JWKS outage should not immediately break a
			// still-valid, unexpired delegation. Short expiry bounds the staleness.
			return s.cached, nil
		}
		return nil, fmt.Errorf("%w: JWKS fetch %s: %v", ErrKeyLookup, s.url, err)
	}
	s.cached = fetched
	s.fetchedAt = now
	return s.cached, nil
}

// fetchJWKS retrieves and parses a JWKS document over the SSRF-guarded client.
func fetchJWKS(ctx context.Context, client *http.Client, url string) ([][]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes))
	if err != nil {
		return nil, err
	}
	return parseJWKS(body)
}

// jwk is the subset of an RFC 7517 JSON Web Key we consume: an Ed25519 public
// key. Every other key type and use is skipped, not errored, so an issuer can
// publish a mixed keyset without breaking gemot.
type jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Use string `json:"use"`
}

type jwksDoc struct {
	Keys []jwk `json:"keys"`
}

// parseJWKS extracts every usable Ed25519 signing key from a JWKS document.
// Individual malformed or non-matching keys are skipped; the function errors only
// when the document itself is unparseable or yields no usable key.
func parseJWKS(body []byte) ([][]byte, error) {
	var doc jwksDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("invalid JWKS JSON: %w", err)
	}
	out := make([][]byte, 0, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "OKP" || k.Crv != "Ed25519" {
			continue // not an Ed25519 key
		}
		if k.Use != "" && k.Use != "sig" {
			continue // key explicitly not for signing
		}
		raw, err := decodeJWKBase64(k.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue // skip a single bad key rather than reject the whole set
		}
		out = append(out, raw)
		if len(out) >= maxJWKSKeys {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("JWKS contains no usable Ed25519 signing keys")
	}
	return out, nil
}

// decodeJWKBase64 decodes a JWK member. RFC 7517 mandates base64url without
// padding; we accept padded input too, since some issuers emit it.
func decodeJWKBase64(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty")
	}
	if raw, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return raw, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// newSSRFGuardedClient builds an HTTP client suitable for fetching a JWKS from an
// operator-configured URL. Unless allowPrivate is set, it refuses to connect to
// any non-public address — the actual resolved connect address is checked, so a
// public hostname that resolves or redirects to an internal IP is still blocked
// (T4). Redirects are not followed at all, and every timeout is tight.
func newSSRFGuardedClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: jwksDialTimeout}
	if !allowPrivate {
		dialer.Control = blockPrivateDial
	}
	return &http.Client{
		Timeout: jwksFetchTimeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   jwksDialTimeout,
			ResponseHeaderTimeout: jwksFetchTimeout,
			MaxIdleConns:          8,
			IdleConnTimeout:       30 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("jwks: refusing to follow redirect to %s", req.URL.Redacted())
		},
	}
}

// blockPrivateDial is a net.Dialer.Control hook that rejects a connection whose
// resolved address is not a public unicast address. Because it runs after DNS
// resolution on the address actually being dialed, it defends against DNS
// rebinding and redirect-to-internal, not just literal private URLs.
func blockPrivateDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("jwks: cannot parse dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("jwks: dial address %q is not an IP", host)
	}
	if isBlockedIP(ip) {
		return fmt.Errorf("jwks: refusing to connect to non-public address %s (SSRF guard)", ip)
	}
	return nil
}

// extraBlockedCIDRs are ranges not covered by the net.IP predicates below but
// still unsafe as a fetch target: carrier-grade NAT and the various
// documentation/benchmarking reservations that sometimes front internal
// infrastructure.
var extraBlockedCIDRs = func() []*net.IPNet {
	raw := []string{
		"100.64.0.0/10",   // RFC 6598 carrier-grade NAT
		"192.0.0.0/24",    // RFC 6890 IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // RFC 2544 benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
	}
	nets := make([]*net.IPNet, 0, len(raw))
	for _, c := range raw {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// isBlockedIP reports whether ip is anything other than a public, routable
// unicast address. IPv4-mapped IPv6 addresses are handled by the standard
// predicates, which evaluate the embedded v4 address.
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() ||
		ip.IsPrivate() {
		return true
	}
	for _, n := range extraBlockedCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// validateJWKSURL checks a configured JWKS URL at startup: it must be an https
// URL with a host. The scheme requirement is unconditional — key material must
// travel over TLS — and is not relaxed by allowPrivate, which only widens the
// permitted address ranges for internal issuers.
func validateJWKSURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("empty JWKS URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("JWKS URL is not a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("JWKS URL must be https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("JWKS URL has no host")
	}
	return nil
}
