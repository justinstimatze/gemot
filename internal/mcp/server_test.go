package mcp

import "testing"

func TestCoerceVoteValue(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    int
		wantErr bool
	}{
		{"float64 1", float64(1), 1, false},
		{"float64 0", float64(0), 0, false},
		{"float64 -1", float64(-1), -1, false},
		{"int 1", int(1), 1, false},
		{"int 0", int(0), 0, false},
		{"int -1", int(-1), -1, false},
		{"string 1", "1", 1, false},
		{"string +1", "+1", 1, false},
		{"string 0", "0", 0, false},
		{"string -1", "-1", -1, false},
		{"nil", nil, 0, false},
		{"invalid string", "agree", 0, true},
		{"string 2", "2", 0, true},
		{"bool", true, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := coerceVoteValue(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("coerceVoteValue(%v): error = %v, wantErr = %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("coerceVoteValue(%v) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
