package principal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	// PublicKey is the issuer's ed25519 signing key. The issuer — not the
	// principal — signs Credential.SigningPayload() with it.
	PublicKey []byte `json:"public_key"`

	// Algo is the signature algorithm. Empty means ed25519; Phase 1 accepts only
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

// IssuerVerifier verifies credentials signed by a configured RemoteIssuer. It
// implements Verifier. It looks up the ISSUER's key (not the principal's),
// enforces namespace binding and local-shadow rejection, and otherwise reuses
// the exact checks LocalVerifier applies.
type IssuerVerifier struct {
	issuers map[string]RemoteIssuer

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
	iss, ok := v.issuers[cred.Issuer]
	if !ok {
		// A "local"/empty issuer must never reach here — routing sends those to
		// LocalVerifier. Reaching here with an untrusted label is fail-closed.
		return nil, fmt.Errorf("%w: %q", ErrIssuerUnknown, cred.Issuer)
	}
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

	// Signature verifies against the ISSUER's key, over the same canonical bytes
	// LocalVerifier reconstructs. The issuer label is part of those bytes, so a
	// credential cannot be relabelled onto a different (more trusted) issuer.
	if err := auth.Verify(iss.algoOrDefault(), iss.PublicKey, cred.SigningPayload(), cred.Signature); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVerifyFailed, err)
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
// or match-all namespace, namespaces that overlap across different issuers, or
// an invalid key. An empty issuer set yields a router that behaves exactly like
// the local verifier alone.
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
func NewRoutingVerifier(local Verifier, issuers []RemoteIssuer) (*RoutingVerifier, error) {
	if local == nil {
		return nil, errors.New("principal: NewRoutingVerifier requires a local verifier")
	}
	if len(issuers) == 0 {
		return &RoutingVerifier{local: local}, nil
	}

	byName := make(map[string]RemoteIssuer, len(issuers))
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
			return nil, fmt.Errorf("%w: issuer %q uses unsupported algo %q (Phase 1 is ed25519-only)", ErrMalformed, iss.Name, iss.Algo)
		}
		if err := auth.ValidatePublicKey(auth.AlgoEd25519, iss.PublicKey); err != nil {
			return nil, fmt.Errorf("%w: issuer %q public_key: %v", ErrMalformed, iss.Name, err)
		}
		byName[iss.Name] = iss
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

	iv := &IssuerVerifier{issuers: byName}
	if lv, ok := local.(*LocalVerifier); ok {
		iv.localLookup = lv.Lookup
	}
	return &RoutingVerifier{local: local, remote: iv}, nil
}
