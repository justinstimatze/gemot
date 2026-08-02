package main

import "testing"

// TestParseSlotLenient pins the fix for the silent parser miss: a compromise
// that writes "Friday at 14:00" (full day name + "at") must still resolve to
// the "Fri 14:00" slot, not be scored as a non-commit.
func TestParseSlotLenient(t *testing.T) {
	in := Instance{Days: 5, PerDay: 4} // grid labels: Mon..Fri x 09:00/11:00/14:00/16:00
	want := Slot(4*4 + 2)              // Fri 14:00

	for _, txt := range []string{
		"The group should meet Fri 14:00.",
		"The group should schedule the meeting for Friday at 14:00.",
		"Recommendation: Friday 14:00 works for everyone.",
	} {
		got, ok := parseSlot(in, txt)
		if !ok || got != want {
			t.Errorf("parseSlot(%q) = (%v, %v), want (%v, true)", txt, got, ok, want)
		}
	}
	if _, ok := parseSlot(in, "no concrete time is named in this sentence"); ok {
		t.Error("expected no match when no slot is named")
	}
	// earliest-occurring wins when two are named
	got, ok := parseSlot(in, "prefer Mon 09:00 over Fri 14:00")
	if !ok || got != Slot(0) {
		t.Errorf("earliest-slot rule: got (%v,%v), want (0,true)", got, ok)
	}
}
