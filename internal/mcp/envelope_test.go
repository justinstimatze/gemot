package mcp

import (
	"testing"
)

func TestParseEnvelopeMode(t *testing.T) {
	cases := []struct {
		in      string
		want    EnvelopeMode
		wantErr bool
	}{
		{"", EnvelopeOff, false},
		{"off", EnvelopeOff, false},
		{"advisory", EnvelopeAdvisory, false},
		{"required", EnvelopeRequired, false},
		{"REQUIRED", EnvelopeOff, true},
		{"strict", EnvelopeOff, true},
	}
	for _, c := range cases {
		got, err := ParseEnvelopeMode(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseEnvelopeMode(%q) err=%v wantErr=%v", c.in, err, c.wantErr)
		}
		if got != c.want {
			t.Errorf("ParseEnvelopeMode(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
