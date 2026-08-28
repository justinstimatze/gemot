package store

import (
	"context"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestMemoryOAuthAuthorizationCodeConsumeOnce(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()
	oc := &deliberation.OAuthAuthorizationCode{
		Code: "gac_test1", AgentID: "agent-1", Principal: "oauthkey:abc",
		CodeChallenge: "chal", CodeChallengeMethod: "S256",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	if err := m.CreateOAuthAuthorizationCode(ctx, oc); err != nil {
		t.Fatalf("CreateOAuthAuthorizationCode: %v", err)
	}

	got, err := m.ConsumeOAuthAuthorizationCode(ctx, "gac_test1", "agent-1")
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if got.Principal != "oauthkey:abc" || got.CodeChallenge != "chal" {
		t.Errorf("consumed row = %+v, missing expected fields", got)
	}

	if _, err := m.ConsumeOAuthAuthorizationCode(ctx, "gac_test1", "agent-1"); err == nil {
		t.Fatal("second consume of the same code should fail")
	}
}

// TestMemoryOAuthAuthorizationCodeFailureModes covers the three ways a
// consume can fail: unknown code, wrong client_id, expired — asserted
// against the same generic error shape ConsumeOAuthAuthorizationCode's doc
// comment requires (no oracle for a token-endpoint response).
func TestMemoryOAuthAuthorizationCodeFailureModes(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore()

	t.Run("unknown code", func(t *testing.T) {
		if _, err := m.ConsumeOAuthAuthorizationCode(ctx, "gac_nope", "agent-1"); err == nil {
			t.Fatal("expected error for unknown code")
		}
	})

	t.Run("wrong client_id", func(t *testing.T) {
		oc := &deliberation.OAuthAuthorizationCode{
			Code: "gac_wrongclient", AgentID: "agent-1", Principal: "oauthkey:abc",
			CodeChallenge: "chal", CodeChallengeMethod: "S256",
			ExpiresAt: time.Now().Add(10 * time.Minute),
		}
		if err := m.CreateOAuthAuthorizationCode(ctx, oc); err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := m.ConsumeOAuthAuthorizationCode(ctx, "gac_wrongclient", "agent-2"); err == nil {
			t.Fatal("expected error when client_id does not match the code's agent_id")
		}
	})

	t.Run("expired", func(t *testing.T) {
		oc := &deliberation.OAuthAuthorizationCode{
			Code: "gac_expired", AgentID: "agent-1", Principal: "oauthkey:abc",
			CodeChallenge: "chal", CodeChallengeMethod: "S256",
			ExpiresAt: time.Now().Add(-time.Minute),
		}
		if err := m.CreateOAuthAuthorizationCode(ctx, oc); err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := m.ConsumeOAuthAuthorizationCode(ctx, "gac_expired", "agent-1"); err == nil {
			t.Fatal("expected error for expired code")
		}
	})
}
