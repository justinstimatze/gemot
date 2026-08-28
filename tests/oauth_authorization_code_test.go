package tests

import (
	"context"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
)

func TestPostgresOAuthAuthorizationCodeConsumeOnce(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()
	oc := &deliberation.OAuthAuthorizationCode{
		Code: "gac_pgtest1", AgentID: "agent-1", Principal: "oauthkey:abc",
		CodeChallenge: "chal", CodeChallengeMethod: "S256",
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	if err := db.CreateOAuthAuthorizationCode(ctx, oc); err != nil {
		t.Fatalf("CreateOAuthAuthorizationCode: %v", err)
	}

	got, err := db.ConsumeOAuthAuthorizationCode(ctx, "gac_pgtest1", "agent-1")
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if got.Principal != "oauthkey:abc" || got.CodeChallenge != "chal" {
		t.Errorf("consumed row = %+v, missing expected fields", got)
	}

	if _, err := db.ConsumeOAuthAuthorizationCode(ctx, "gac_pgtest1", "agent-1"); err == nil {
		t.Fatal("second consume of the same code should fail (atomic UPDATE ... WHERE consumed_at IS NULL matched zero rows)")
	}
}

// TestPostgresOAuthAuthorizationCodeFailureModes mirrors
// TestMemoryOAuthAuthorizationCodeFailureModes in internal/store — both
// backends must reject the same three cases (unknown code, wrong client_id,
// expired) with no externally distinguishable difference between them.
func TestPostgresOAuthAuthorizationCodeFailureModes(t *testing.T) {
	db := tempDB(t)
	ctx := context.Background()

	t.Run("unknown code", func(t *testing.T) {
		if _, err := db.ConsumeOAuthAuthorizationCode(ctx, "gac_pgnope", "agent-1"); err == nil {
			t.Fatal("expected error for unknown code")
		}
	})

	t.Run("wrong client_id", func(t *testing.T) {
		oc := &deliberation.OAuthAuthorizationCode{
			Code: "gac_pgwrongclient", AgentID: "agent-1", Principal: "oauthkey:abc",
			CodeChallenge: "chal", CodeChallengeMethod: "S256",
			ExpiresAt: time.Now().Add(10 * time.Minute),
		}
		if err := db.CreateOAuthAuthorizationCode(ctx, oc); err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := db.ConsumeOAuthAuthorizationCode(ctx, "gac_pgwrongclient", "agent-2"); err == nil {
			t.Fatal("expected error when client_id does not match the code's agent_id")
		}
	})

	t.Run("expired", func(t *testing.T) {
		oc := &deliberation.OAuthAuthorizationCode{
			Code: "gac_pgexpired", AgentID: "agent-1", Principal: "oauthkey:abc",
			CodeChallenge: "chal", CodeChallengeMethod: "S256",
			ExpiresAt: time.Now().Add(-time.Minute),
		}
		if err := db.CreateOAuthAuthorizationCode(ctx, oc); err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := db.ConsumeOAuthAuthorizationCode(ctx, "gac_pgexpired", "agent-1"); err == nil {
			t.Fatal("expected error for expired code")
		}
	})
}
