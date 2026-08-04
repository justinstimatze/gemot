package deliberation

import "testing"

func TestFreeformTemplateExists(t *testing.T) {
	tmpl, ok := GetTemplate("freeform")
	if !ok {
		t.Fatal("freeform template not registered")
	}
	if tmpl.Name != "freeform" {
		t.Errorf("name = %q, want freeform", tmpl.Name)
	}
	// It is the unstructured control: no heavy governance rules beyond a floor.
	if tmpl.AnalysisHint == "" {
		t.Error("freeform should carry a neutral analysis hint")
	}
	if _, hasSecond := tmpl.DefaultRules["require_second"]; hasSecond {
		t.Error("freeform must not impose parliamentary rules")
	}

	// It must appear in the catalog.
	found := false
	for _, tt := range ListTemplates() {
		if tt.Name == "freeform" {
			found = true
		}
	}
	if !found {
		t.Error("freeform missing from ListTemplates()")
	}
}
