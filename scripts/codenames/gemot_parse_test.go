package main

import (
	"reflect"
	"testing"
)

func bWords(words ...string) Board { return Board{Words: words} }

func TestParseGuesses(t *testing.T) {
	tests := []struct {
		name  string
		board Board
		text  string
		limit int
		want  []string
	}{
		{
			// Regression: single-paragraph compromise must not be discarded just
			// because a later clause mentions the assassin/opponent.
			name:  "single paragraph, warning in later clause",
			board: bWords("WASHINGTON", "CONDITION", "FLAG", "GOVERNMENT", "SPACE"),
			text:  `The clue "state" most confidently covers WASHINGTON, CONDITION, and FLAG. Do not guess SPACE — it could be the assassin.`,
			limit: 4,
			want:  []string{"WASHINGTON", "CONDITION", "FLAG"},
		},
		{
			name:  "explicit FINAL line preferred",
			board: bWords("PORT", "AMAZON", "DROP", "TRAIN", "SPACE"),
			text:  "Lots of reasoning here mentioning SPACE and TRAIN in prose.\nFINAL: PORT, AMAZON, DROP",
			limit: 4,
			want:  []string{"PORT", "AMAZON", "DROP"},
		},
		{
			name:  "token boundary: ICE not matched inside PRICE",
			board: bWords("PRICE", "ICE"),
			text:  "The clue points to PRICE.",
			limit: 3,
			want:  []string{"PRICE"},
		},
		{
			name:  "cap at limit",
			board: bWords("A", "B", "C", "D"),
			text:  "Guess A then B then C then D.",
			limit: 2,
			want:  []string{"A", "B"},
		},
		{
			name:  "avoid clause excludes its word",
			board: bWords("TRUNK", "NUT", "SPACE"),
			text:  "Strong picks are TRUNK and NUT. Avoid SPACE.",
			limit: 3,
			want:  []string{"TRUNK", "NUT"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGuesses(tt.board, tt.text, tt.limit)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseGuesses = %v, want %v", got, tt.want)
			}
		})
	}
}
