package principal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/justinstimatze/gemot/internal/auth"
)

// This file adds the "remote trust root": verifying delegation credentials
// minted by an external issuer whose *issuer* key is trusted, rather than by
// the principal itself (which is what LocalVerifier does).
//
// The trust model is different in kind from LocalVerifier and the difference is
// the whole risk surface. LocalVerifier is self-authenticating: the principal
// signs its own delegation and gemot holds the principal's key. An external
// issuer instead *attests* "principal P delegated to agent A", and gemot trusts
// the issuer to have authenticated P. That trust transfer introduces one attack
// LocalVerifier cannot have: an issuer vouching for a principal it does not own,
// including shadowing a locally-registered principal.
//
// Two controls contain it, both enforced here:
//
//   - Namespace binding (a SPIFFE trust domain, in effect): each issuer may only
//     vouch for principals under a prefix it declares, and those prefixes are
//     pairwise-disjoint across issuers (checked at construction, fail-closed).
//   - Local-shadow rejection: a remote issuer may never vouch for a principal
//     that has a local key of its own — that principal is self-sovereign.
//
// This is Phase 1 of docs/remote-trust-root.md: issuer keys are pinned in
// config, so there is no network on the verification path (no JWKS, no SSRF, no
// fetch-DoS). Everything else — expiry, scope attenuation, agent binding, and
// the service-layer proof-of-possession — is unchanged from the local path.

// RemoteIssuer describes one trusted external authority. Its JSON encoding is
// the wire format of a single GEMOT_TRUSTED_ISSUERS entry; PublicKey is base64
// in JSON, which is the standard encoding/json treatment of a []byte field.
type RemoteIssuer struct {
	// Name identifies the issuer and MUST equal Credential.Issuer for the
	// credentials it mints. It is routed on and, because it is inside the
	// signed payload, cannot be forged to a different issuer.
	Name string `json:"name"`

	// Namespaces are the principal prefixes this issuer may vouch for. At least
	// one is required; the empty prefix (which would match every principal) is
	// rejected at construction.
	Namespaces []string `json:"namespaces"`

	// PublicKey is the issuer's ed25519 signing key, pinned in config (Phase 1).
	// The issuer — not the principal — signs Credential.SigningPayload() with it.
	// Exactly one of PublicKey or JWKSURL must be set per issuer.
	PublicKey []byte `json:"public_key,omitempty"`

	// JWKSURL is an https endpoint publishing the issuer's Ed25519 signing keys
	// as a JWKS (Phase 2). When set, keys are resolved and cached from it instead
	// of pinned in config, which is what lets an issuer rotate keys without a
	// gemot config change. Exactly one of PublicKey or JWKSURL must be set.
	JWKSURL string `json:"jwks_url,omitempty"`

	// Algo is the signature algorithm. Empty means ed25519; Phase 1/2 accept only
	// ed25519.
	Algo string `json:"algo,omitempty"`
}

// covers reports whether this issuer is authorized to vouch for principal.
func (ri RemoteIssuer) covers(principal string) bool {
	for _, ns := range ri.Namespaces {
		if ns != "" && strings.HasPrefix(principal, ns) {
			return true
		}
	}
	return false
}

func (ri RemoteIssuer) algoOrDefault() string {
	if ri.Algo == "" {
		return auth.AlgoEd25519
	}
	return ri.Algo
}

// ParseIssuers decodes the GEMOT_TRUSTED_ISSUERS JSON array. An empty string
// returns (nil, nil): federation is simply off. A malformed value returns an
// error so startup can fail closed rather than silently disable federation.
func ParseIssuers(raw string) ([]RemoteIssuer, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var issuers []RemoteIssuer
	if err := json.Unmarshal([]byte(raw), &issuers); err != nil {
		return nil, fmt.Errorf("principal: GEMOT_TRUSTED_ISSUERS is not a valid JSON issuer array: %w", err)
	}
	return issuers, nil
}

// issuerEntry pairs an issuer's static metadata (name, namespaces, algo) with
// its runtime key source (a pinned key, or a JWKS cache).
type issuerEntry struct {
	meta   RemoteIssuer
	source keySource
}

// IssuerVerifier verifies credentials signed by a configured RemoteIssuer. It
// implements Verifier. It looks up the ISSUER's key (not the principal's),
// enforces namespace binding and local-shadow rejection, and otherwise reuses
// the exact checks LocalVerifier applies.
type IssuerVerifier struct {
	issuers map[string]issuerEntry

	// localLookup resolves a principal to its local key, used only to detect a
	// remote issuer trying to shadow a locally-registered principal. nil skips
	// the shadow check (unit tests without a registry); production always sets
	// it so the check is live.
	localLookup KeyLookup

	// Now is injectable for tests. nil uses time.Now.
	Now func() time.Time
}

func (v *IssuerVerifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *IssuerVerifier) known(name string) bool {
	_, ok := v.issuers[name]
	return ok
}

// Verify establishes that a credential minted by a trusted issuer authorizes
// agentID to speak for cred.Principal in t.
//
// Checks run cheapest-first: shape, issuer trust, agent binding, expiry, scope,
// and namespace are all in-memory; the signature is one ed25519 op; the
// local-shadow lookup (the only one that may touch a datastore) runs last.
func (v *IssuerVerifier) Verify(ctx context.Context, cred *Credential, agentID string, t Target) (*Result, error) {
	if err := cred.Validate(); err != nil {
		return nil, err
	}
	entry, ok := v.issuers[cred.Issuer]
	if !ok {
		// A "local"/empty issuer must never reach here — routing sends those to
		// LocalVerifier. Reaching here with an untrusted label is fail-closed.
		return nil, fmt.Errorf("%w: %q", ErrIssuerUnknown, cred.Issuer)
	}
	iss := entry.meta
	if cred.Agent != agentID {
		return nil, fmt.Errorf("%w: credential authorizes %q, presented by %q",
			ErrAgentMismatch, cred.Agent, agentID)
	}
	if !cred.ExpiresAt.After(v.now()) {
		return nil, fmt.Errorf("%w: expired at %s", ErrExpired, cred.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if !cred.CoversTarget(t) {
		return nil, fmt.Errorf("%w: scope %q does not cover deliberation %q",
			ErrScopeMismatch, cred.Scope, t.DeliberationID)
	}
	if !iss.covers(cred.Principal) {
		return nil, fmt.Errorf("%w: issuer %q may not vouch for principal %q",
			ErrIssuerNamespace, cred.Issuer, cred.Principal)
	}

	// Resolve the issuer's current signing key(s). For a pinned issuer this is
	// the one config key and never fails; for a JWKS issuer it is the cached
	// keyset (fetched over the SSRF-guarded client, fail-closed to ErrKeyLookup).
	// Key resolution runs only after every cheap in-memory check has passed, so a
	// malformed or out-of-scope credential can never drive a network fetch.
	candidates, err := entry.source.keys(ctx)
	if err != nil {
		return nil, err // already ErrKeyLookup-wrapped
	}

	// Signature verifies against the ISSUER's key(s), over the same canonical
	// bytes LocalVerifier reconstructs. The issuer label is part of those bytes,
	// so a credential cannot be relabelled onto a different (more trusted) issuer.
	// Trying every published key is how key rotation is tolerated: during a
	// rotation the issuer publishes old and new keys together.
	if !anyKeyVerifies(iss.algoOrDefault(), candidates, cred) {
		return nil, fmt.Errorf("%w: no trusted key for issuer %q verifies this credential", ErrVerifyFailed, cred.Issuer)
	}

	// Local-shadow rejection: a principal that holds a local key is
	// self-sovereign; no external issuer may speak for it, even from within its
	// own namespace. A registry outage fails closed rather than skipping the
	// check.
	if v.localLookup != nil {
		_, _, err := v.localLookup(ctx, cred.Principal)
		switch {
		case errors.Is(err, ErrNoKey):
			// Good: nothing local to shadow.
		case err == nil:
			return nil, fmt.Errorf("%w: principal %q is locally registered and cannot be spoken for by a remote issuer",
				ErrIssuerNamespace, cred.Principal)
		default:
			return nil, fmt.Errorf("%w for %q: %w", ErrKeyLookup, cred.Principal, err)
		}
	}

	return &Result{
		Principal: cred.Principal,
		Agent:     cred.Agent,
		AgentKey:  cred.AgentKey,
		Scope:     cred.Scope,
		Issuer:    cred.Issuer,
		ExpiresAt: cred.ExpiresAt,
	}, nil
}

// anyKeyVerifies reports whether cred's signature verifies under any of the
// candidate keys. It is constant in structure (no early information leak matters
// here — a failing signature is a failing signature) and short-circuits on the
// first match.
func anyKeyVerifies(algo string, candidates [][]byte, cred *Credential) bool {
	payload := cred.SigningPayload()
	for _, pk := range candidates {
		if auth.Verify(algo, pk, payload, cred.Signature) == nil {
			return true
		}
	}
	return false
}

// remoteOptions configures the network behavior of JWKS-backed issuers. Defaults
// are production-safe: private addresses blocked, a 5-minute cache TTL, real
// clock, and a fresh SSRF-guarded client.
type remoteOptions struct {
	httpClient   *http.Client
	allowPrivate bool
	cacheTTL     time.Duration
	now          func() time.Time
}

// Option customizes NewRoutingVerifier. All options are only meaningful when at
// least one issuer uses a jwks_url.
type Option func(*remoteOptions)

// WithJWKSAllowPrivate permits JWKS fetches to non-public (loopback/private/
// link-local) addresses. Off by default; intended for local testing or an
// internal issuer on a private network. https is still required regardless.
func WithJWKSAllowPrivate(allow bool) Option {
	return func(o *remoteOptions) { o.allowPrivate = allow }
}

// WithJWKSCacheTTL sets how long fetched issuer keys are cached before a refresh
// is attempted. Non-positive values are ignored (the default is kept).
func WithJWKSCacheTTL(ttl time.Duration) Option {
	return func(o *remoteOptions) {
		if ttl > 0 {
			o.cacheTTL = ttl
		}
	}
}

// WithJWKSHTTPClient overrides the HTTP client used for JWKS fetches. Intended
// for tests (pointing at a TLS test server); production uses the built-in
// SSRF-guarded client and should not set this.
func WithJWKSHTTPClient(c *http.Client) Option {
	return func(o *remoteOptions) { o.httpClient = c }
}

// WithClock overrides the time source used for credential-expiry checks and JWKS
// cache staleness. For tests.
func WithClock(now func() time.Time) Option {
	return func(o *remoteOptions) { o.now = now }
}

// RoutingVerifier dispatches on the (signed) issuer label: "" or "local" to the
// local verifier, a trusted issuer name to the remote verifier, and anything
// else to a fail-closed rejection. It implements Verifier and is what
// SetPrincipalVerifier installs when trusted issuers are configured.
type RoutingVerifier struct {
	local  Verifier
	remote *IssuerVerifier // nil when no issuers are configured
}

func (r *RoutingVerifier) Verify(ctx context.Context, cred *Credential, agentID string, t Target) (*Result, error) {
	switch cred.Issuer {
	case "", IssuerLocal:
		return r.local.Verify(ctx, cred, agentID, t)
	default:
		if r.remote == nil || !r.remote.known(cred.Issuer) {
			return nil, fmt.Errorf("%w: %q", ErrIssuerUnknown, cred.Issuer)
		}
		return r.remote.Verify(ctx, cred, agentID, t)
	}
}

// NewRoutingVerifier wraps a local verifier with a set of trusted external
// issuers. It validates the issuer set and returns an error — intended to abort
// startup — if the set is unsafe: a duplicate or reserved issuer name, a missing
// or match-all namespace, namespaces that overlap across different issuers, an
// issuer that sets neither or both of public_key/jwks_url, or an invalid key or
// JWKS URL. An empty issuer set yields a router that behaves exactly like the
// local verifier alone.
//
// Options tune JWKS behavior (cache TTL, private-address policy, HTTP client,
// clock); they are only meaningful when some issuer uses a jwks_url.
//
// If local is a *LocalVerifier, its key lookup is reused for the local-shadow
// check so a remote issuer cannot override a locally-registered principal.
//
// SECURITY: the local-shadow check is the T1 control, and it is only active when
// local is a *LocalVerifier (the sole local backend today, and what the startup
// wiring passes). If a future caller passes a different Verifier as local while
// trusting external issuers, that control is silently absent — pass a
// *LocalVerifier, or extend this to require an explicit KeyLookup, rather than
// relying on a non-LocalVerifier local backend.
func NewRoutingVerifier(local Verifier, issuers []RemoteIssuer, opts ...Option) (*RoutingVerifier, error) {
	if local == nil {
		return nil, errors.New("principal: NewRoutingVerifier requires a local verifier")
	}
	if len(issuers) == 0 {
		return &RoutingVerifier{local: local}, nil
	}

	o := remoteOptions{cacheTTL: defaultJWKSCacheTTL, now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	if o.now == nil {
		o.now = time.Now
	}
	httpClient := o.httpClient
	if httpClient == nil {
		httpClient = newSSRFGuardedClient(o.allowPrivate)
	}

	byName := make(map[string]issuerEntry, len(issuers))
	for _, iss := range issuers {
		switch {
		case iss.Name == "":
			return nil, fmt.Errorf("%w: issuer name is required", ErrMalformed)
		case iss.Name == IssuerLocal:
			return nil, fmt.Errorf("%w: issuer name %q is reserved for the local verifier", ErrMalformed, IssuerLocal)
		case len(iss.Name) > MaxIssuerLen:
			return nil, fmt.Errorf("%w: issuer name %q exceeds %d characters", ErrMalformed, iss.Name, MaxIssuerLen)
		}
		if _, dup := byName[iss.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate issuer name %q", ErrMalformed, iss.Name)
		}
		if len(iss.Namespaces) == 0 {
			return nil, fmt.Errorf("%w: issuer %q declares no namespaces", ErrMalformed, iss.Name)
		}
		for _, ns := range iss.Namespaces {
			if ns == "" {
				return nil, fmt.Errorf("%w: issuer %q declares an empty namespace, which would match every principal", ErrMalformed, iss.Name)
			}
		}
		if iss.algoOrDefault() != auth.AlgoEd25519 {
			return nil, fmt.Errorf("%w: issuer %q uses unsupported algo %q (Phase 1/2 is ed25519-only)", ErrMalformed, iss.Name, iss.Algo)
		}

		// Exactly one key source: a pinned key (Phase 1) or a JWKS URL (Phase 2).
		// Neither is unusable; both is ambiguous — reject both, fail-closed.
		hasKey := len(iss.PublicKey) > 0
		hasJWKS := strings.TrimSpace(iss.JWKSURL) != ""
		switch {
		case hasKey && hasJWKS:
			return nil, fmt.Errorf("%w: issuer %q sets both public_key and jwks_url; use exactly one", ErrMalformed, iss.Name)
		case !hasKey && !hasJWKS:
			return nil, fmt.Errorf("%w: issuer %q sets neither public_key nor jwks_url", ErrMalformed, iss.Name)
		case hasKey:
			if err := auth.ValidatePublicKey(auth.AlgoEd25519, iss.PublicKey); err != nil {
				return nil, fmt.Errorf("%w: issuer %q public_key: %v", ErrMalformed, iss.Name, err)
			}
			byName[iss.Name] = issuerEntry{meta: iss, source: staticKeySource{key: iss.PublicKey}}
		default: // hasJWKS
			if err := validateJWKSURL(iss.JWKSURL); err != nil {
				return nil, fmt.Errorf("%w: issuer %q jwks_url: %v", ErrMalformed, iss.Name, err)
			}
			byName[iss.Name] = issuerEntry{meta: iss, source: &jwksKeySource{
				url:    strings.TrimSpace(iss.JWKSURL),
				client: httpClient,
				ttl:    o.cacheTTL,
				now:    o.now,
			}}
		}
	}

	// Namespaces must be disjoint across different issuers: if one issuer's
	// prefix is a prefix of another's, a principal could fall under both and the
	// trust domains would overlap.
	for i := range issuers {
		for j := range issuers {
			if i == j || issuers[i].Name == issuers[j].Name {
				continue
			}
			for _, a := range issuers[i].Namespaces {
				for _, b := range issuers[j].Namespaces {
					if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
						return nil, fmt.Errorf("%w: issuers %q and %q have overlapping namespaces (%q vs %q)",
							ErrMalformed, issuers[i].Name, issuers[j].Name, a, b)
					}
				}
			}
		}
	}

	iv := &IssuerVerifier{issuers: byName, Now: o.now}
	if lv, ok := local.(*LocalVerifier); ok {
		iv.localLookup = lv.Lookup
	}
	return &RoutingVerifier{local: local, remote: iv}, nil
}

// Prewarm fetches every JWKS-backed issuer's keys once, so the first credential
// on the hot path finds a warm cache instead of paying a synchronous fetch. It
// returns one error per issuer that could not be pre-warmed; callers should log
// these but need not abort — a JWKS endpoint that is transiently down at startup
// will be retried on first use and, until then, its credentials fail closed.
// Issuers with pinned keys are skipped (nothing to fetch).
func (r *RoutingVerifier) Prewarm(ctx context.Context) []error {
	if r.remote == nil {
		return nil
	}
	var errs []error
	for name, entry := range r.remote.issuers {
		js, ok := entry.source.(*jwksKeySource)
		if !ok {
			continue
		}
		if _, err := js.keys(ctx); err != nil {
			errs = append(errs, fmt.Errorf("issuer %q: %w", name, err))
		}
	}
	return errs
}
