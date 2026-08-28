package mcp

import (
	"testing"

	"github.com/justinstimatze/gemot/internal/payments"
)

// TestResolveModelFallsBackWhenDefaultModelUnset covers the stdio
// transport, which never sets mppCfg at all: resolveModel must still
// return something sane rather than an empty string.
func TestResolveModelFallsBackWhenDefaultModelUnset(t *testing.T) {
	s := &server{}
	if got := s.resolveModel(""); got == "" {
		t.Error("resolveModel(\"\") returned empty with no DefaultModel configured")
	}
}

// TestResolveModelUsesConfiguredDefault is the regression test for the
// code-review finding that resolveModel hardcoded "claude-sonnet-4-6" as
// the MPP scope-binding default instead of reading GEMOT_MODEL (via
// mppCfg.DefaultModel, set from it in RunHTTP) -- if GEMOT_MODEL were ever
// changed, an analyze call with an empty Model field would invoke the
// newly configured model but bind its MPP credential to the stale literal.
func TestResolveModelUsesConfiguredDefault(t *testing.T) {
	s := &server{mppCfg: payments.Config{DefaultModel: "claude-opus-4-6"}}
	if got := s.resolveModel(""); got != "claude-opus-4-6" {
		t.Errorf("resolveModel(\"\") = %q, want the configured default %q", got, "claude-opus-4-6")
	}
	if got := s.resolveModel("claude-haiku-4-5"); got != "claude-haiku-4-5" {
		t.Errorf("resolveModel with an explicit model = %q, want it passed through unchanged", got)
	}
}
