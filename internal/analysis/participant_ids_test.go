package analysis

import (
	"strings"
	"testing"
)

// TestExtractParticipantIDsMatchesFormatClaimsForCrux guards against the
// regex/emitter drift that silently broke {{PARTICIPANT_IDS}} for an
// unknown span of time: formatClaimsForCrux emits participant="N" but
// the regex was looking for speaker="N", so the extracted list was
// always empty and the LLM was free to hallucinate participant IDs.
// Pin the contract: the function must recognize the attribute name
// the emitter actually writes.
func TestExtractParticipantIDsMatchesFormatClaimsForCrux(t *testing.T) {
	claims := []claim{
		{AgentNum: "0", Claim: "a"},
		{AgentNum: "1", Quote: "q", Claim: "b"},
		{AgentNum: "2", Sources: []claimSource{{Quote: "q2"}}, Claim: "c"},
		{AgentNum: "0", Claim: "d"},
	}
	rendered := formatClaimsForCrux(claims)
	got := extractParticipantIDs(rendered)

	for _, want := range []string{"0", "1", "2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("extractParticipantIDs(%q) = %q; missing participant %q", rendered, got, want)
		}
	}
	if strings.Count(got, "0") != 1 {
		t.Fatalf("expected participant 0 deduped to one occurrence; got %q", got)
	}
}

// TestExtractParticipantIDsEmptyOnNoMatches documents the empty-list
// case — if the regex ever drifts again, this test still passes but
// the matched-emitter test above fails, pointing at the real problem.
func TestExtractParticipantIDsEmptyOnNoMatches(t *testing.T) {
	if got := extractParticipantIDs("<claim>no attrs</claim>"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
