package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/payments"
	"github.com/justinstimatze/gemot/internal/store"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestHandleAnalyzeToolExpertPanelRefundsSandboxQuotaOnFailure is the
// regression test for the code-review finding that a sandbox (unauthenticated)
// caller's daily quota slot was permanently consumed by gateSandbox even
// when the paid action failed downstream — unlike a paying (apiKey) caller,
// whose credits ARE refunded via AddCredits on the same failure path.
//
// Uses a quota of exactly 1 call: if the first failed call's slot isn't
// refunded, the second call is denied by gateSandbox itself (a distinct,
// distinguishable error message) before ever reaching the actual validation
// error this test triggers. Tool-level errors come back as a
// *CallToolResult with IsError set (the MCP convention), not as the
// function's Go error return — see errResult.
func TestHandleAnalyzeToolExpertPanelRefundsSandboxQuotaOnFailure(t *testing.T) {
	backend := store.NewMemoryStore()
	svc := deliberation.NewService(backend, nil)
	s := &server{svc: svc, sandboxQuota: payments.NewSandboxQuota(1, time.Hour)}
	ctx := context.WithValue(context.Background(), payments.ContextKeyClientIP{}, "203.0.113.5")

	callWithBogusSourceType := func() string {
		t.Helper()
		res, _, err := s.handleAnalyzeTool(ctx, nil, analyzeToolParams{
			Action:     "expert_panel",
			Document:   "some document text to review",
			SourceType: "not-a-real-source-type",
		})
		if err != nil {
			t.Fatalf("unexpected transport-level error: %v", err)
		}
		if res == nil || !res.IsError || len(res.Content) == 0 {
			t.Fatalf("expected a tool-level error result, got %+v", res)
		}
		tc, ok := res.Content[0].(*sdkmcp.TextContent)
		if !ok {
			t.Fatalf("content[0] is %T, want *TextContent", res.Content[0])
		}
		return tc.Text
	}

	msg1 := callWithBogusSourceType()
	if strings.Contains(msg1, "sandbox daily quota exceeded") {
		t.Fatalf("first call should fail on source_type validation, not the quota: %v", msg1)
	}

	msg2 := callWithBogusSourceType()
	if strings.Contains(msg2, "sandbox daily quota exceeded") {
		t.Fatalf("sandbox quota slot was not refunded after the first failure: %v", msg2)
	}
}

// TestHandleAnalyzeToolReframeIsGatedForSandboxCallers is the regression
// test for the code-review finding that "reframe" was the one analyze
// action that never called checkMPPCredential/gateSandbox: an
// unauthenticated caller could trigger it for free, bounded only by the
// generic IP rate limit rather than the 20/day sandbox quota every other
// paid action enforces.
//
// Uses a quota of exactly 1 and a real, succeeding reframe call (a failing
// one would get its slot refunded by the sandbox-refund fix, which would
// mask exactly what this test needs to observe): the first call consumes
// the sandbox slot; the second must now be denied by gateSandbox itself.
// Before the fix, neither call ever touched the sandbox quota at all.
func TestHandleAnalyzeToolReframeIsGatedForSandboxCallers(t *testing.T) {
	backend := store.NewMemoryStore()
	svc := deliberation.NewService(backend, nil)
	svc.SetReframer(fakeReframer{})
	s := &server{svc: svc, sandboxQuota: payments.NewSandboxQuota(1, time.Hour)}
	ctx := context.WithValue(context.Background(), payments.ContextKeyClientIP{}, "203.0.113.9")

	d, err := svc.CreateDeliberation(ctx, "reframe gate test", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	pos, err := svc.SubmitPosition(ctx, d.ID, "alice", "the original position")
	if err != nil {
		t.Fatalf("SubmitPosition: %v", err)
	}

	reframe := func() (*sdkmcp.CallToolResult, error) {
		res, _, err := s.handleAnalyzeTool(ctx, nil, analyzeToolParams{
			Action: "reframe", DeliberationID: d.ID, PositionID: pos.ID,
		})
		return res, err
	}

	res1, err1 := reframe()
	if err1 != nil || res1 == nil || res1.IsError {
		t.Fatalf("first sandbox call should succeed and consume the quota slot: err=%v res=%+v", err1, res1)
	}

	_, err2 := reframe()
	if err2 == nil {
		t.Fatal("second call should be denied by the now-exhausted sandbox quota")
	}
	if !strings.Contains(err2.Error(), "sandbox daily quota exceeded") {
		t.Fatalf("expected the sandbox gate to deny the second call, got: %v", err2)
	}
}

// fakeReframer is a minimal deliberation.Reframer for tests that need
// CoreReframe to actually succeed (a nil analyzer leaves Service.reframer
// unset, so ReframePosition always fails with "reframing not available" --
// which would itself get refunded by the sandbox-refund fix, making it
// impossible to observe a genuinely CONSUMED quota slot without this).
type fakeReframer struct{}

func (fakeReframer) Reframe(_ context.Context, position, _, _ string) (string, error) {
	return "reframed: " + position, nil
}
