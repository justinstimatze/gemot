package principal

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
)

// jwksJSON renders a JWKS document publishing the given Ed25519 public keys.
func jwksJSON(pubs ...ed25519.PublicKey) string {
	entries := make([]string, len(pubs))
	for i, p := range pubs {
		entries[i] = fmt.Sprintf(`{"kty":"OKP","crv":"Ed25519","use":"sig","x":%q}`,
			base64.RawURLEncoding.EncodeToString(p))
	}
	return `{"keys":[` + strings.Join(entries, ",") + `]}`
}

// jwksTestServer is a TLS test server serving a swappable JWKS body with a
// request counter, so tests can assert rotation and fetch rate-limiting.
type jwksTestServer struct {
	*httptest.Server
	mu     sync.Mutex
	body   string
	status int
	hits   int
}

func newJWKSTestServer(body string) *jwksTestServer {
	s := &jwksTestServer{body: body, status: http.StatusOK}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		body, status := s.body, s.status
		s.hits++
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	return s
}

func (s *jwksTestServer) set(body string, status int) {
	s.mu.Lock()
	s.body, s.status = body, status
	s.mu.Unlock()
}

func (s *jwksTestServer) hitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

// --- parseJWKS ---

func TestParseJWKS(t *testing.T) {
	pub1, _, _ := ed25519.GenerateKey(nil)
	pub2, _, _ := ed25519.GenerateKey(nil)

	t.Run("extracts ed25519 signing keys", func(t *testing.T) {
		got, err := parseJWKS([]byte(jwksJSON(pub1, pub2)))
		if err != nil {
			t.Fatalf("parseJWKS: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d keys, want 2", len(got))
		}
	})

	t.Run("skips non-OKP and non-sig keys, keeps the usable one", func(t *testing.T) {
		doc := `{"keys":[
			{"kty":"RSA","n":"abc","e":"AQAB"},
			{"kty":"OKP","crv":"X25519","x":"` + base64.RawURLEncoding.EncodeToString(pub1) + `"},
			{"kty":"OKP","crv":"Ed25519","use":"enc","x":"` + base64.RawURLEncoding.EncodeToString(pub1) + `"},
			{"kty":"OKP","crv":"Ed25519","use":"sig","x":"` + base64.RawURLEncoding.EncodeToString(pub2) + `"}
		]}`
		got, err := parseJWKS([]byte(doc))
		if err != nil {
			t.Fatalf("parseJWKS: %v", err)
		}
		if len(got) != 1 || !ed25519.PublicKey(got[0]).Equal(pub2) {
			t.Fatalf("got %d keys, want the single Ed25519 sig key", len(got))
		}
	})

	t.Run("accepts padded base64", func(t *testing.T) {
		doc := fmt.Sprintf(`{"keys":[{"kty":"OKP","crv":"Ed25519","x":%q}]}`,
			base64.URLEncoding.EncodeToString(pub1)) // padded
		got, err := parseJWKS([]byte(doc))
		if err != nil || len(got) != 1 {
			t.Fatalf("parseJWKS(padded) = (%d keys, %v)", len(got), err)
		}
	})

	t.Run("empty keyset errors", func(t *testing.T) {
		if _, err := parseJWKS([]byte(`{"keys":[]}`)); err == nil {
			t.Fatal("parseJWKS(empty) = nil, want error")
		}
		if _, err := parseJWKS([]byte(`{"keys":[{"kty":"RSA"}]}`)); err == nil {
			t.Fatal("parseJWKS(no usable) = nil, want error")
		}
	})

	t.Run("malformed JSON errors", func(t *testing.T) {
		if _, err := parseJWKS([]byte(`not json`)); err == nil {
			t.Fatal("parseJWKS(garbage) = nil, want error")
		}
	})

	t.Run("wrong-length key is skipped", func(t *testing.T) {
		doc := `{"keys":[{"kty":"OKP","crv":"Ed25519","x":"` +
			base64.RawURLEncoding.EncodeToString([]byte("too-short")) + `"}]}`
		if _, err := parseJWKS([]byte(doc)); err == nil {
			t.Fatal("parseJWKS(short key) = nil, want error (no usable keys)")
		}
	})
}

// --- SSRF guard ---

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},              // loopback
		{"::1", true},                    // loopback v6
		{"169.254.169.254", true},        // link-local (cloud metadata)
		{"10.0.0.5", true},               // private
		{"172.16.0.1", true},             // private
		{"192.168.1.1", true},            // private
		{"100.64.0.1", true},             // CGNAT
		{"0.0.0.0", true},                // unspecified
		{"fd00::1", true},                // IPv6 ULA (private)
		{"198.18.0.1", true},             // benchmarking
		{"::ffff:169.254.169.254", true}, // v4-mapped metadata
		{"8.8.8.8", false},               // public
		{"1.1.1.1", false},               // public
		{"2606:4700:4700::1111", false},  // public v6
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := isBlockedIP(ip); got != c.blocked {
			t.Errorf("isBlockedIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

func TestBlockPrivateDial(t *testing.T) {
	if err := blockPrivateDial("tcp", "127.0.0.1:443", nil); err == nil {
		t.Error("blockPrivateDial(loopback) = nil, want error")
	}
	if err := blockPrivateDial("tcp", "169.254.169.254:80", nil); err == nil {
		t.Error("blockPrivateDial(metadata) = nil, want error")
	}
	if err := blockPrivateDial("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("blockPrivateDial(public) = %v, want nil", err)
	}
}

// The default (non-allowPrivate) client refuses to connect to a loopback JWKS —
// the connection is blocked at dial time, before any TLS or HTTP exchange (T4).
func TestSSRFClientBlocksLoopback(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	ts := newJWKSTestServer(jwksJSON(pub))
	defer ts.Close()

	client := newSSRFGuardedClient(false)
	_, err := fetchJWKS(context.Background(), client, ts.URL)
	if err == nil {
		t.Fatal("fetchJWKS to loopback = nil, want SSRF block")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("error = %v, want SSRF-guard rejection", err)
	}
	if ts.hitCount() != 0 {
		t.Fatalf("server was hit %d times, want 0 (blocked before connect)", ts.hitCount())
	}
}

// --- validateJWKSURL ---

func TestValidateJWKSURL(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		{"https://issuer.example/.well-known/jwks.json", false},
		{"http://issuer.example/jwks.json", true}, // must be https
		{"ftp://issuer.example/jwks.json", true},
		{"https://", true}, // no host
		{"", true},
		{"   ", true},
		{"://bad", true},
	}
	for _, c := range cases {
		err := validateJWKSURL(c.url)
		if c.wantErr && err == nil {
			t.Errorf("validateJWKSURL(%q) = nil, want error", c.url)
		}
		if !c.wantErr && err != nil {
			t.Errorf("validateJWKSURL(%q) = %v, want nil", c.url, err)
		}
	}
}

// --- jwksKeySource cache behavior ---

// newTestSource builds a jwksKeySource pointed at ts, trusting its test cert,
// with an injectable clock.
func newTestSource(ts *jwksTestServer, ttl time.Duration, now func() time.Time) *jwksKeySource {
	return &jwksKeySource{url: ts.URL, client: ts.Client(), ttl: ttl, now: now}
}

func TestJWKSKeySourceFetchesAndCaches(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	ts := newJWKSTestServer(jwksJSON(pub))
	defer ts.Close()

	clock := time.Unix(1000, 0)
	src := newTestSource(ts, time.Minute, func() time.Time { return clock })

	got, err := src.keys(context.Background())
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(got) != 1 || !ed25519.PublicKey(got[0]).Equal(pub) {
		t.Fatalf("keys = %d, want the served key", len(got))
	}
	// A second call within TTL is served from cache — no new fetch.
	if _, err := src.keys(context.Background()); err != nil {
		t.Fatalf("keys (cached): %v", err)
	}
	if ts.hitCount() != 1 {
		t.Fatalf("server hit %d times, want 1 (second call cached)", ts.hitCount())
	}
}

// Rotation: an issuer publishes old+new together, then retires the old key. The
// cache picks up the overlap after TTL, so credentials from either key verify
// during the window; once the old key is dropped, its credentials stop (T6).
func TestJWKSRotation(t *testing.T) {
	pubA, privA, _ := ed25519.GenerateKey(nil)
	pubB, privB, _ := ed25519.GenerateKey(nil)
	ts := newJWKSTestServer(jwksJSON(pubA))
	defer ts.Close()

	clock := time.Unix(1000, 0)
	iss := RemoteIssuer{Name: remIssuer, Namespaces: []string{remNS}, JWKSURL: ts.URL, Algo: auth.AlgoEd25519}
	rv, err := NewRoutingVerifier(localNone(), []RemoteIssuer{iss},
		WithJWKSHTTPClient(ts.Client()),
		WithJWKSCacheTTL(time.Minute),
		WithClock(func() time.Time { return clock }))
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	target := Target{DeliberationID: testDelib}

	// Only key A is published: A verifies, B does not.
	if _, err := rv.Verify(context.Background(), fedCred(t, privA, remPrincipal), testAgent, target); err != nil {
		t.Fatalf("credential A pre-rotation = %v, want nil", err)
	}
	if _, err := rv.Verify(context.Background(), fedCred(t, privB, remPrincipal), testAgent, target); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("credential B pre-rotation = %v, want ErrVerifyFailed", err)
	}

	// Issuer publishes A and B together; advance past TTL to force a refresh.
	ts.set(jwksJSON(pubA, pubB), http.StatusOK)
	clock = clock.Add(2 * time.Minute)
	if _, err := rv.Verify(context.Background(), fedCred(t, privA, remPrincipal), testAgent, target); err != nil {
		t.Fatalf("credential A during overlap = %v, want nil", err)
	}
	if _, err := rv.Verify(context.Background(), fedCred(t, privB, remPrincipal), testAgent, target); err != nil {
		t.Fatalf("credential B during overlap = %v, want nil", err)
	}

	// Issuer retires A; after TTL, A's credentials stop verifying.
	ts.set(jwksJSON(pubB), http.StatusOK)
	clock = clock.Add(2 * time.Minute)
	if _, err := rv.Verify(context.Background(), fedCred(t, privB, remPrincipal), testAgent, target); err != nil {
		t.Fatalf("credential B post-rotation = %v, want nil", err)
	}
	if _, err := rv.Verify(context.Background(), fedCred(t, privA, remPrincipal), testAgent, target); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("credential A post-rotation = %v, want ErrVerifyFailed (key retired)", err)
	}
}

// A JWKS that has never been reachable fails closed: verification returns
// ErrKeyLookup, never a pass (T5).
func TestJWKSFailsClosedWhenUnreachable(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	ts := newJWKSTestServer("")
	ts.set("", http.StatusInternalServerError)
	defer ts.Close()

	clock := time.Unix(1000, 0)
	src := newTestSource(ts, time.Minute, func() time.Time { return clock })
	if _, err := src.keys(context.Background()); !errors.Is(err, ErrKeyLookup) {
		t.Fatalf("keys(down, no cache) = %v, want ErrKeyLookup", err)
	}
	_ = priv
}

// After a successful fetch, a transient outage serves the last-good keys rather
// than breaking a still-valid delegation.
func TestJWKSServesStaleOnTransientOutage(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	ts := newJWKSTestServer(jwksJSON(pub))
	defer ts.Close()

	clock := time.Unix(1000, 0)
	src := newTestSource(ts, time.Minute, func() time.Time { return clock })
	if _, err := src.keys(context.Background()); err != nil {
		t.Fatalf("initial keys: %v", err)
	}

	// Endpoint goes down; advance past TTL so a refresh is attempted and fails.
	ts.set("", http.StatusInternalServerError)
	clock = clock.Add(2 * time.Minute)
	got, err := src.keys(context.Background())
	if err != nil {
		t.Fatalf("keys(stale) = %v, want last-good served", err)
	}
	if len(got) != 1 || !ed25519.PublicKey(got[0]).Equal(pub) {
		t.Fatalf("stale keys = %d, want the last-good key", len(got))
	}
	_ = priv
}

// A flood of failing fetches cannot amplify into outbound requests: with no
// usable cache, attempts are rate-limited to at most one per TTL (T5).
func TestJWKSRateLimitsFailingFetches(t *testing.T) {
	ts := newJWKSTestServer("")
	ts.set("", http.StatusInternalServerError)
	defer ts.Close()

	clock := time.Unix(1000, 0)
	src := newTestSource(ts, time.Minute, func() time.Time { return clock })

	for i := 0; i < 5; i++ {
		if _, err := src.keys(context.Background()); !errors.Is(err, ErrKeyLookup) {
			t.Fatalf("attempt %d = %v, want ErrKeyLookup", i, err)
		}
	}
	if ts.hitCount() != 1 {
		t.Fatalf("server hit %d times within one TTL, want 1 (rate-limited)", ts.hitCount())
	}
	// After TTL, exactly one more attempt is allowed.
	clock = clock.Add(2 * time.Minute)
	_, _ = src.keys(context.Background())
	if ts.hitCount() != 2 {
		t.Fatalf("server hit %d times after TTL, want 2", ts.hitCount())
	}
}

// End-to-end: a JWKS-backed issuer verifies a federated credential through the
// full RoutingVerifier, and namespace binding still applies.
func TestRoutingVerifierJWKSBacked(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	ts := newJWKSTestServer(jwksJSON(pub))
	defer ts.Close()

	iss := RemoteIssuer{Name: remIssuer, Namespaces: []string{remNS}, JWKSURL: ts.URL, Algo: auth.AlgoEd25519}
	rv, err := NewRoutingVerifier(localNone(), []RemoteIssuer{iss}, WithJWKSHTTPClient(ts.Client()))
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	target := Target{DeliberationID: testDelib}

	res, err := rv.Verify(context.Background(), fedCred(t, priv, remPrincipal), testAgent, target)
	if err != nil {
		t.Fatalf("Verify = %v, want nil", err)
	}
	if res.Principal != remPrincipal || res.Issuer != remIssuer {
		t.Fatalf("Result = {%q,%q}, want {%q,%q}", res.Principal, res.Issuer, remPrincipal, remIssuer)
	}
	// Namespace binding is unchanged by the key source.
	if _, err := rv.Verify(context.Background(), fedCred(t, priv, "other:mallory"), testAgent, target); !errors.Is(err, ErrIssuerNamespace) {
		t.Fatalf("out-of-namespace via JWKS = %v, want ErrIssuerNamespace", err)
	}
}

func TestNewRoutingVerifierJWKSValidation(t *testing.T) {
	goodPub, _, _ := ed25519.GenerateKey(nil)
	tests := []struct {
		name    string
		iss     RemoteIssuer
		wantErr bool
	}{
		{"jwks only ok", RemoteIssuer{Name: "a", Namespaces: []string{"a:"}, JWKSURL: "https://a.example/jwks"}, false},
		{"key only ok", RemoteIssuer{Name: "a", Namespaces: []string{"a:"}, PublicKey: goodPub}, false},
		{"both set", RemoteIssuer{Name: "a", Namespaces: []string{"a:"}, PublicKey: goodPub, JWKSURL: "https://a.example/jwks"}, true},
		{"neither set", RemoteIssuer{Name: "a", Namespaces: []string{"a:"}}, true},
		{"http jwks rejected", RemoteIssuer{Name: "a", Namespaces: []string{"a:"}, JWKSURL: "http://a.example/jwks"}, true},
		{"garbage jwks rejected", RemoteIssuer{Name: "a", Namespaces: []string{"a:"}, JWKSURL: "://nope"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRoutingVerifier(localNone(), []RemoteIssuer{tc.iss})
			if tc.wantErr && err == nil {
				t.Fatalf("NewRoutingVerifier(%s) = nil, want error", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("NewRoutingVerifier(%s) = %v, want nil", tc.name, err)
			}
		})
	}
}

func TestPrewarm(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	up := newJWKSTestServer(jwksJSON(pub))
	defer up.Close()
	down := newJWKSTestServer("")
	down.set("", http.StatusInternalServerError)
	defer down.Close()

	issUp := RemoteIssuer{Name: remIssuer, Namespaces: []string{remNS}, JWKSURL: up.URL, Algo: auth.AlgoEd25519}
	issDown := RemoteIssuer{Name: "https://beta.example", Namespaces: []string{"beta:"}, JWKSURL: down.URL, Algo: auth.AlgoEd25519}

	// Both issuers share one injected client (both are TLS test servers with
	// their own certs, so use a client that trusts either — here, skip verify via
	// each server's client isn't possible; instead point each at its own via
	// separate verifiers).
	rvUp, err := NewRoutingVerifier(localNone(), []RemoteIssuer{issUp}, WithJWKSHTTPClient(up.Client()))
	if err != nil {
		t.Fatalf("NewRoutingVerifier(up): %v", err)
	}
	if errs := rvUp.Prewarm(context.Background()); len(errs) != 0 {
		t.Fatalf("Prewarm(up) = %v, want no errors", errs)
	}
	if up.hitCount() != 1 {
		t.Fatalf("prewarm hit up %d times, want 1", up.hitCount())
	}

	rvDown, err := NewRoutingVerifier(localNone(), []RemoteIssuer{issDown}, WithJWKSHTTPClient(down.Client()))
	if err != nil {
		t.Fatalf("NewRoutingVerifier(down): %v", err)
	}
	if errs := rvDown.Prewarm(context.Background()); len(errs) != 1 {
		t.Fatalf("Prewarm(down) = %v, want exactly one error", errs)
	}
}

// Prewarm on a local-only (no remote) router is a no-op.
func TestPrewarmLocalOnly(t *testing.T) {
	rv, err := NewRoutingVerifier(localNone(), nil)
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	if errs := rv.Prewarm(context.Background()); errs != nil {
		t.Fatalf("Prewarm(local-only) = %v, want nil", errs)
	}
}
