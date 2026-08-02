package main

import "testing"

// TestParseSlotLenient pins the fix for the silent parser miss: a compromise
// that writes "Friday at 14:00" (full day name + "at") must still resolve to
// the "Fri 14:00" slot, not be scored as a non-commit.
func TestParseSlotLenient(t *testing.T) {
	in := Instance{Days: 7, PerDay: 12} // 7 days x 12 hourly slots (08:00..19:00)
	want := Slot(4*12 + 6)              // Fri 14:00 (day 4, 14:00 = index 6)

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
	if !ok || got != Slot(1) { // Mon 09:00 = day 0, 09:00 = index 1
		t.Errorf("earliest-slot rule: got (%v,%v), want (1,true)", got, ok)
	}
}
