package deliberation

import (
	"testing"

	"github.com/justinstimatze/gemot/types"
)

// PrincipalRollup is the consumer of Position.PrincipalVerified that
// decides whose reputation a round credits (Move 5). Its security invariant
// — only verified delegations roll up — is what stops an agent from
// redirecting standing onto a principal it doesn't speak for, so it's worth
// pinning directly rather than only through the LLM-gated analysis path.
func TestPrincipalRollup(t *testing.T) {
	cases := []struct {
		name      string
		positions []types.Position
		want      map[string]string
	}{
		{
			name: "verified delegation rolls up to principal",
			positions: []types.Position{
				{AgentID: "acme-agent", OnBehalfOf: "acme:alice", PrincipalVerified: true},
			},
			want: map[string]string{"acme-agent": "acme:alice"},
		},
		{
			name: "unverified on_behalf_of does NOT roll up (hijack guard)",
			positions: []types.Position{
				{AgentID: "attacker", OnBehalfOf: "acme:alice", PrincipalVerified: false},
			},
			want: map[string]string{},
		},
		{
			name: "no on_behalf_of credits the agent itself",
			positions: []types.Position{
				{AgentID: "solo", PrincipalVerified: true},
			},
			want: map[string]string{},
		},
		{
			name: "two agents for one principal both roll up (collapse)",
			positions: []types.Position{
				{AgentID: "agent-1", OnBehalfOf: "acme:alice", PrincipalVerified: true},
				{AgentID: "agent-2", OnBehalfOf: "acme:alice", PrincipalVerified: true},
			},
			want: map[string]string{"agent-1": "acme:alice", "agent-2": "acme:alice"},
		},
		{
			name: "same principal across an agent's positions is stable",
			positions: []types.Position{
				{AgentID: "agent", OnBehalfOf: "acme:alice", PrincipalVerified: true},
				{AgentID: "agent", OnBehalfOf: "acme:alice", PrincipalVerified: true},
			},
			want: map[string]string{"agent": "acme:alice"},
		},
		{
			name: "one agent, two principals in a round is ambiguous → dropped",
			positions: []types.Position{
				{AgentID: "agent", OnBehalfOf: "acme:alice", PrincipalVerified: true},
				{AgentID: "agent", OnBehalfOf: "acme:bob", PrincipalVerified: true},
			},
			want: map[string]string{},
		},
		{
			name: "conflicted agent stays dropped even if a later position agrees",
			positions: []types.Position{
				{AgentID: "agent", OnBehalfOf: "acme:alice", PrincipalVerified: true},
				{AgentID: "agent", OnBehalfOf: "acme:bob", PrincipalVerified: true},
				{AgentID: "agent", OnBehalfOf: "acme:alice", PrincipalVerified: true},
			},
			want: map[string]string{},
		},
		{
			name: "verified and unverified for the same agent: verified wins, no conflict",
			positions: []types.Position{
				{AgentID: "agent", OnBehalfOf: "acme:alice", PrincipalVerified: true},
				{AgentID: "agent", OnBehalfOf: "acme:eve", PrincipalVerified: false},
			},
			want: map[string]string{"agent": "acme:alice"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PrincipalRollup(tc.positions)
			if len(got) != len(tc.want) {
				t.Fatalf("rollup = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("rollup[%q] = %q, want %q (full: %v)", k, got[k], v, got)
				}
			}
		})
	}
}
