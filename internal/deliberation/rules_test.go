package deliberation

import "testing"

// TestRuleInt pins the JSON-unmarshal-compatible pattern where rules
// land as float64 (default for json.Unmarshal → map[string]any), plus
// native int and the absent/nil fallbacks. A regression here breaks
// every rule-gated feature (speaking_time_limit, min_participants,
// threshold overrides) silently, so it's worth keeping mechanical.
func TestRuleInt(t *testing.T) {
	cases := []struct {
		name   string
		rules  map[string]any
		key    string
		def    int
		want   int
	}{
		{"nil rules returns default", nil, "k", 42, 42},
		{"missing key returns default", map[string]any{"other": 1}, "k", 7, 7},
		{"float64 (JSON default) round-trips", map[string]any{"k": float64(5)}, "k", 0, 5},
		{"native int round-trips", map[string]any{"k": 5}, "k", 0, 5},
		{"wrong type returns default", map[string]any{"k": "5"}, "k", 9, 9},
		{"bool value returns default", map[string]any{"k": true}, "k", 9, 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &Deliberation{Rules: c.rules}
			if got := RuleInt(d, c.key, c.def); got != c.want {
				t.Errorf("RuleInt = %d, want %d", got, c.want)
			}
		})
	}
}

// TestRuleBool exercises the boolean rule path. Same invariant as
// RuleInt: a misclassified bool rule silently disables
// require_second / other boolean gates.
func TestRuleBool(t *testing.T) {
	cases := []struct {
		name   string
		rules  map[string]any
		key    string
		def    bool
		want   bool
	}{
		{"nil rules returns default (true)", nil, "k", true, true},
		{"nil rules returns default (false)", nil, "k", false, false},
		{"missing key returns default", map[string]any{"other": true}, "k", true, true},
		{"bool true round-trips", map[string]any{"k": true}, "k", false, true},
		{"bool false round-trips", map[string]any{"k": false}, "k", true, false},
		{"string returns default", map[string]any{"k": "true"}, "k", false, false},
		{"number returns default", map[string]any{"k": 1.0}, "k", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &Deliberation{Rules: c.rules}
			if got := RuleBool(d, c.key, c.def); got != c.want {
				t.Errorf("RuleBool = %v, want %v", got, c.want)
			}
		})
	}
}
