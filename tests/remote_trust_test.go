package tests

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/principal"
)

// Remote-trust integration tests exercise the RoutingVerifier end-to-end
// through SubmitPosition: a credential minted by an external issuer (issuer key
// trusted in config) rather than self-signed by the principal, wired exactly as
// main.go wires it. See docs/remote-trust-root.md (Phase 1) and
// internal/principal/remote.go.

const (
	remoteIssuer    = "https://acme.example"
	remoteNamespace = "acme:"
	remotePrincipal = "acme:alice"
	remoteAgent     = "acme-agent"
)

// installRemoteIssuer wraps the service's default local verifier in a
// RoutingVerifier trusting one external issuer, returning the issuer's signing
// key. This mirrors the wiring in main.go.
func installRemoteIssuer(t *testing.T, svc *deliberation.Service) (issuerPriv, agentPriv ed25519.PrivateKey, agentPub ed25519.PublicKey) {
	t.Helper()
	ipub, ipriv := newKeypair(t)
	iss := principal.RemoteIssuer{
		Name:       remoteIssuer,
		Namespaces: []string{remoteNamespace},
		PublicKey:  ipub,
		Algo:       "ed25519",
	}
	rv, err := principal.NewRoutingVerifier(svc.PrincipalVerifier(), []principal.RemoteIssuer{iss})
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	svc.SetPrincipalVerifier(rv)

	// The agent proves possession against a locally-registered key: even under
	// federation the *agent* still registers, only the *principal* need not.
	apub, apriv := newKeypair(t)
	if err := svc.RegisterAgentKey(context.Background(), remoteAgent, apub, "ed25519"); err != nil {
		t.Fatalf("register agent key: %v", err)
	}
	return ipriv, apriv, apub
}

// A credential signed by a trusted external issuer — for a principal with no
// gemot key of its own — verifies end-to-end.
func TestRemoteTrust_FederatedCredentialAccepted(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	issuerPriv, agentPriv, agentPub := installRemoteIssuer(t, svc)

	cred := mintCredential(t, issuerPriv, principal.Credential{
		Principal: remotePrincipal,
		Agent:     remoteAgent,
		AgentKey:  agentPub,
		Issuer:    remoteIssuer,
	})

	d, err := svc.CreateDeliberation(ctx, "Federated", "", deliberation.WithPrincipalPolicy("required"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "delegated via an external trust root"
	p, err := svc.SubmitPosition(ctx, d.ID, remoteAgent, content,
		deliberation.WithOnBehalfOf(remotePrincipal),
		deliberation.WithPrincipalCredential(cred),
		deliberation.WithSignature(signPosition(t, agentPriv, remoteAgent, d.ID, d.Round, content)))
	if err != nil {
		t.Fatalf("submit federated credential: %v", err)
	}
	if !p.PrincipalVerified {
		t.Error("PrincipalVerified = false for a valid federated credential, want true")
	}

	positions, err := svc.GetPositions(ctx, d.ID, nil, nil)
	if err != nil {
		t.Fatalf("get positions: %v", err)
	}
	if len(positions) != 1 || !positions[0].PrincipalVerified {
		t.Fatalf("federated verification did not persist: %+v", positions)
	}
}

// Phase 2, end-to-end: the issuer's key is resolved from a JWKS endpoint rather
// than pinned in config, and a credential it signs verifies through the full
// submit path — proving key rotation via JWKS composes with proof-of-possession.
func TestRemoteTrust_JWKSBackedCredentialAccepted(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// A TLS JWKS endpoint publishing the issuer's Ed25519 signing key.
	issuerPub, issuerPriv := newKeypair(t)
	jwks := fmt.Sprintf(`{"keys":[{"kty":"OKP","crv":"Ed25519","use":"sig","x":%q}]}`,
		base64.RawURLEncoding.EncodeToString(issuerPub))
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(jwks))
	}))
	defer ts.Close()

	iss := principal.RemoteIssuer{
		Name:       remoteIssuer,
		Namespaces: []string{remoteNamespace},
		JWKSURL:    ts.URL,
		Algo:       "ed25519",
	}
	rv, err := principal.NewRoutingVerifier(svc.PrincipalVerifier(),
		[]principal.RemoteIssuer{iss}, principal.WithJWKSHTTPClient(ts.Client()))
	if err != nil {
		t.Fatalf("NewRoutingVerifier: %v", err)
	}
	svc.SetPrincipalVerifier(rv)

	// The agent still proves possession against a locally-registered key.
	agentPub, agentPriv := newKeypair(t)
	if err := svc.RegisterAgentKey(ctx, remoteAgent, agentPub, "ed25519"); err != nil {
		t.Fatalf("register agent key: %v", err)
	}

	cred := mintCredential(t, issuerPriv, principal.Credential{
		Principal: remotePrincipal,
		Agent:     remoteAgent,
		AgentKey:  agentPub,
		Issuer:    remoteIssuer,
	})

	d, err := svc.CreateDeliberation(ctx, "JWKS federated", "", deliberation.WithPrincipalPolicy("required"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "delegated via a JWKS-resolved issuer key"
	p, err := svc.SubmitPosition(ctx, d.ID, remoteAgent, content,
		deliberation.WithOnBehalfOf(remotePrincipal),
		deliberation.WithPrincipalCredential(cred),
		deliberation.WithSignature(signPosition(t, agentPriv, remoteAgent, d.ID, d.Round, content)))
	if err != nil {
		t.Fatalf("submit JWKS-backed credential: %v", err)
	}
	if !p.PrincipalVerified {
		t.Error("PrincipalVerified = false for a valid JWKS-backed credential, want true")
	}
}

// A captured federated credential is inert: an attacker who lifts it (from an
// export, from get_positions) but does not control the agent's private key
// cannot submit under it. This is proof-of-possession biting on the remote path.
func TestRemoteTrust_LeakedCredentialIsInert(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	issuerPriv, _, agentPub := installRemoteIssuer(t, svc)

	// A valid federated credential, then leaked to an attacker.
	cred := mintCredential(t, issuerPriv, principal.Credential{
		Principal: remotePrincipal,
		Agent:     remoteAgent,
		AgentKey:  agentPub,
		Issuer:    remoteIssuer,
	})

	d, err := svc.CreateDeliberation(ctx, "Leak", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The attacker claims the credential's agent_id but signs with a key it
	// controls — which is not the one registered for remoteAgent.
	_, attackerPriv := newKeypair(t)
	const content = "I stole a credential"
	_, err = svc.SubmitPosition(ctx, d.ID, remoteAgent, content,
		deliberation.WithOnBehalfOf(remotePrincipal),
		deliberation.WithPrincipalCredential(cred),
		deliberation.WithSignature(signPosition(t, attackerPriv, remoteAgent, d.ID, d.Round, content)))
	if err == nil {
		t.Fatal("leaked federated credential accepted, want rejection")
	}
	if !strings.Contains(err.Error(), "SIGNATURE_VERIFY_FAIL") && !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want a verification failure", err)
	}
}

// The central control (T1), end-to-end: a remote issuer may not vouch for a
// principal that is locally registered, even from within its own namespace. The
// shadow check runs against the real store-backed key lookup here.
func TestRemoteTrust_RejectsShadowingLocalPrincipal(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	issuerPriv, agentPriv, agentPub := installRemoteIssuer(t, svc)

	// Register a LOCAL key for the very principal the remote issuer wants to
	// speak for. That principal is now self-sovereign.
	registerPrincipal(t, svc, remotePrincipal)

	cred := mintCredential(t, issuerPriv, principal.Credential{
		Principal: remotePrincipal,
		Agent:     remoteAgent,
		AgentKey:  agentPub,
		Issuer:    remoteIssuer,
	})

	d, err := svc.CreateDeliberation(ctx, "Shadow", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const content = "trying to shadow a local principal"
	_, err = svc.SubmitPosition(ctx, d.ID, remoteAgent, content,
		deliberation.WithOnBehalfOf(remotePrincipal),
		deliberation.WithPrincipalCredential(cred),
		deliberation.WithSignature(signPosition(t, agentPriv, remoteAgent, d.ID, d.Round, content)))
	if err == nil {
		t.Fatal("remote issuer vouched for a locally-registered principal, want rejection")
	}
	if !strings.Contains(err.Error(), "PRINCIPAL_VERIFY_FAIL") {
		t.Errorf("error = %q, want PRINCIPAL_VERIFY_FAIL", err)
	}
}
