package payments

import "testing"

// TestMPPPriceForModel_FlooredAtSPTMinimum verifies that sub-50¢ model
// prices are floored to the Stripe SPT minimum. Stripe rejects SPT charges
// below $0.50; without this floor, Haiku and Sonnet calls would fail at
// settlement and burn the agent's credential.
func TestMPPPriceForModel_FlooredAtSPTMinimum(t *testing.T) {
	cases := []struct {
		model    string
		wantCent int64
	}{
		// Below floor: credit-equivalent is 10¢ and 30¢ respectively;
		// floored to 50¢ to satisfy Stripe SPT minimum.
		{"claude-haiku-4-5", 50},
		{"claude-sonnet-4-6", 50},
		// Above floor: returned unmodified.
		{"claude-opus-4-6", 150},
		// Empty model defaults to Sonnet → 30¢ credit-equivalent → floored to 50¢.
		{"", 50},
		// Unknown model also defaults to Sonnet pricing.
		{"some-future-model", 50},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := MPPPriceForModel(tc.model)
			if got != tc.wantCent {
				t.Errorf("MPPPriceForModel(%q) = %d, want %d", tc.model, got, tc.wantCent)
			}
		})
	}
}

func TestMPPPriceForModel_NeverBelowFloor(t *testing.T) {
	// Property: for ANY model name, the price is at least the SPT minimum.
	// Defense against a future code change that adds a sub-floor tier
	// without remembering to update the floor logic.
	for _, model := range []string{"", "claude-haiku-4-5", "claude-sonnet-4-6", "claude-opus-4-6", "anything"} {
		if got := MPPPriceForModel(model); got < MPPSPTMinimumCents {
			t.Errorf("MPPPriceForModel(%q) = %d, must be ≥ MPPSPTMinimumCents (%d)", model, got, MPPSPTMinimumCents)
		}
	}
}
