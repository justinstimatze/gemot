package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/justinstimatze/gemot/internal/deliberation"
	"github.com/justinstimatze/gemot/internal/store"
)

// fakeChoiceCompromiser records the options/optionVotes it actually
// received, so a test can assert on what reached the "LLM prompt" layer
// without needing a real LLM.
type fakeChoiceCompromiser struct {
	gotOptions []string
	gotVotes   map[string]int
}

func (f *fakeChoiceCompromiser) GenerateCompromise(_ context.Context, _ string, _ *deliberation.AnalysisResult) (string, error) {
	return "compromise", nil
}

func (f *fakeChoiceCompromiser) GenerateCompromiseWithChoice(_ context.Context, _ string, _ *deliberation.AnalysisResult, options []string, optionVotes map[string]int) (string, string, error) {
	f.gotOptions = options
	f.gotVotes = optionVotes
	selected := ""
	if len(options) > 0 {
		selected = options[0]
	}
	return "compromise statement", selected, nil
}

// TestProposeCompromiseWithChoiceAndVotesSanitizesOptions is the regression
// test for the code-review finding that propose_compromise's caller-supplied
// options reached the compromise-generation LLM prompt completely
// unsanitized -- unlike position content, which always goes through
// sanitize.Position first. A PII pattern in an option must be stripped, and
// optionVotes must be rekeyed onto the SANITIZED text so vote counts stay
// aligned with what the generator actually sees.
func TestProposeCompromiseWithChoiceAndVotesSanitizesOptions(t *testing.T) {
	backend := store.NewMemoryStore()
	svc := deliberation.NewService(backend, nil)
	fake := &fakeChoiceCompromiser{}
	svc.SetCompromiseGenerator(fake)
	ctx := context.Background()

	d, err := svc.CreateDeliberation(ctx, "sanitize options test", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	if err := svc.SaveAnalysisResult(ctx, d.ID, d.Round, &deliberation.AnalysisResult{}); err != nil {
		t.Fatalf("SaveAnalysisResult: %v", err)
	}

	const rawOption = "call the vendor at 555-123-4567 to confirm"
	_, _, err = svc.ProposeCompromiseWithChoiceAndVotes(ctx, d.ID, []string{rawOption}, map[string]int{rawOption: 3})
	if err != nil {
		t.Fatalf("ProposeCompromiseWithChoiceAndVotes: %v", err)
	}

	if len(fake.gotOptions) != 1 {
		t.Fatalf("got %d options, want 1", len(fake.gotOptions))
	}
	sanitizedOption := fake.gotOptions[0]
	if strings.Contains(sanitizedOption, "555-123-4567") {
		t.Errorf("option still contains the raw phone number: %q", sanitizedOption)
	}
	if !strings.Contains(sanitizedOption, "[PHONE]") {
		t.Errorf("option = %q, want PII replaced with [PHONE]", sanitizedOption)
	}
	if got, want := fake.gotVotes[sanitizedOption], 3; got != want {
		t.Errorf("gotVotes[sanitized] = %d, want %d (votes must be rekeyed onto the sanitized text)", got, want)
	}
	if _, stillRaw := fake.gotVotes[rawOption]; stillRaw {
		t.Error("optionVotes still keyed by the raw, unsanitized option text")
	}
}

// TestProposeCompromiseNoAnalysisYetOnMemoryStore is the regression test
// for a nil-pointer panic: MemoryStore.GetLatestAnalysisResult returns
// (nil, nil) when no analysis has run yet, unlike Postgres's (nil, err) --
// ProposeCompromise dereferenced the nil result before this fix.
func TestProposeCompromiseNoAnalysisYetOnMemoryStore(t *testing.T) {
	backend := store.NewMemoryStore()
	svc := deliberation.NewService(backend, nil)
	svc.SetCompromiseGenerator(&fakeChoiceCompromiser{})
	ctx := context.Background()

	d, err := svc.CreateDeliberation(ctx, "no analysis yet", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}

	_, err = svc.ProposeCompromise(ctx, d.ID)
	if err == nil {
		t.Fatal("expected an error (no analysis yet), got nil")
	}
	if !strings.Contains(err.Error(), "no analysis results") {
		t.Errorf("error = %q, want a 'no analysis results' message, not a panic", err.Error())
	}
}

// TestGetContextNoAnalysisYetOnMemoryStore is the same regression test as
// above, for GetContext's identical nil-dereference bug.
func TestGetContextNoAnalysisYetOnMemoryStore(t *testing.T) {
	backend := store.NewMemoryStore()
	svc := deliberation.NewService(backend, nil)
	ctx := context.Background()

	d, err := svc.CreateDeliberation(ctx, "no analysis yet", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}

	_, err = svc.GetContext(ctx, d.ID, "alice")
	if err == nil {
		t.Fatal("expected an error (no analysis yet), got nil")
	}
	if !strings.Contains(err.Error(), "no analysis results") {
		t.Errorf("error = %q, want a 'no analysis results' message, not a panic", err.Error())
	}
}

// TestProposeCompromiseWithChoiceAndVotesMapsSelectionBackToOriginal is the
// regression test for the finding that GenerateCompromiseWithChoice can
// only select from the SANITIZED option text, so returning that directly
// broke exact-match comparisons against raw ground truth (e.g. the
// calibration runner). The returned selectedOption must be the original,
// unsanitized text.
func TestProposeCompromiseWithChoiceAndVotesMapsSelectionBackToOriginal(t *testing.T) {
	backend := store.NewMemoryStore()
	svc := deliberation.NewService(backend, nil)
	fake := &fakeChoiceCompromiser{}
	svc.SetCompromiseGenerator(fake)
	ctx := context.Background()

	d, err := svc.CreateDeliberation(ctx, "map selection back test", "")
	if err != nil {
		t.Fatalf("CreateDeliberation: %v", err)
	}
	if err := svc.SaveAnalysisResult(ctx, d.ID, d.Round, &deliberation.AnalysisResult{}); err != nil {
		t.Fatalf("SaveAnalysisResult: %v", err)
	}

	const rawOption = "call the vendor at 555-123-4567 to confirm"
	_, selected, err := svc.ProposeCompromiseWithChoiceAndVotes(ctx, d.ID, []string{rawOption}, map[string]int{rawOption: 1})
	if err != nil {
		t.Fatalf("ProposeCompromiseWithChoiceAndVotes: %v", err)
	}
	if selected != rawOption {
		t.Errorf("selected = %q, want the original raw option %q (fake generator always picks options[0], which was sanitized before being shown to it)", selected, rawOption)
	}
}
