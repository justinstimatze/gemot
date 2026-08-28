package deliberation

import (
	"strings"
	"testing"
)

func TestEscapePipePartRoundTrip(t *testing.T) {
	cases := []string{
		"",
		"plain",
		"has|pipe",
		`has\backslash`,
		`both\|combined`,
		"||double||pipe||",
		`trailing\`,
		"|leading and trailing|",
	}
	for _, want := range cases {
		escaped := escapePipePart(want)
		if strings.Contains(escaped, "|") {
			// every literal pipe must have been escaped to \|
			unescapedPipeCount := strings.Count(escaped, "|") - strings.Count(escaped, `\|`)
			if unescapedPipeCount != 0 {
				t.Errorf("escapePipePart(%q) = %q still contains an unescaped pipe", want, escaped)
			}
		}
		joined := "prefix|" + escaped + "|suffix"
		parts := splitEscapedPipe(joined)
		if len(parts) != 3 {
			t.Fatalf("splitEscapedPipe(%q) = %v, want 3 parts", joined, parts)
		}
		if parts[1] != want {
			t.Errorf("round trip for %q: got %q", want, parts[1])
		}
		if parts[0] != "prefix" || parts[2] != "suffix" {
			t.Errorf("neighboring fields shifted: parts=%v", parts)
		}
	}
}

// TestSplitEscapedPipeMatchesPlainSplitWhenNoSpecialChars confirms the new
// escape-aware parser behaves identically to the old strings.Split(s, "|")
// for every payload that contains no backslash or pipe -- i.e. every
// pre-fix committed log entry that wasn't already exploiting this bug.
func TestSplitEscapedPipeMatchesPlainSplitWhenNoSpecialChars(t *testing.T) {
	payload := "submit_position|deadbeef-0000-0000-0000-000000000000|alice|3|hello world"
	old := strings.Split(payload, "|")
	got := splitEscapedPipe(payload)
	if len(old) != len(got) {
		t.Fatalf("length mismatch: old=%v new=%v", old, got)
	}
	for i := range old {
		if old[i] != got[i] {
			t.Errorf("part %d: old=%q new=%q", i, old[i], got[i])
		}
	}
}
