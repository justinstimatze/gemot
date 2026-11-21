package deliberation

import "testing"

func TestDebateTemplateExists(t *testing.T) {
	tmpl, ok := GetTemplate("debate")
	if !ok {
		t.Fatal("debate template not registered")
	}
	if tmpl.Name != "debate" {
		t.Errorf("name = %q, want debate", tmpl.Name)
	}
	// It must instruct the analysis to separate advocates from the jury.
	if tmpl.AnalysisHint == "" {
		t.Error("debate should carry an analysis hint distinguishing advocates, referee, and jury")
	}
	if _, hasQuorum := tmpl.DefaultRules["min_participants"]; !hasQuorum {
		t.Error("debate should require enough participants for two advocates, a referee, and a jury")
	}

	// It must appear in the catalog.
	found := false
	for _, tt := range ListTemplates() {
		if tt.Name == "debate" {
			found = true
		}
	}
	if !found {
		t.Error("debate missing from ListTemplates()")
	}
}
