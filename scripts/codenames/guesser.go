package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GuessStyle gives guessers distinct reading temperaments so their judgments
// diverge — without diversity there is nothing for aggregation to add.
type GuessStyle struct {
	Name   string
	Prompt string
}

var guessStyles = []GuessStyle{
	{"literal", "You read clues literally and pick the most direct, common associations. You avoid clever stretches."},
	{"lateral", "You favor lateral, figurative, and less obvious associations — puns, categories, second meanings."},
	{"cautious", "You are risk-averse: you weigh the danger of the assassin and opponent words heavily and stop early rather than overreach."},
}

// Guess returns the guesser's ranked words (most to least confident) for a clue,
// plus its short reasoning. The guesser never sees the key.
func (l *LLM) Guess(style GuessStyle, boardWords []string, clue string, n int) ([]string, string, error) {
	user := fmt.Sprintf(`%s

The board words are: %s

The spymaster's clue is "%s" %d — meaning %d of your team's words relate to "%s".

List the board words you would guess, in order from MOST to LEAST confident. Include only words you actually believe are your team's — it is correct to list fewer than %d if you are unsure, since a wrong guess ends the turn and the assassin loses the game. Use only words from the board.

Reply with JSON only:
{"guesses": ["<word>", ...], "reasoning": "<one or two sentences>"}`,
		gameRules, strings.Join(boardWords, ", "), clue, n, n, clue, n)

	system := "You are an expert Codenames guesser. " + style.Prompt
	out, err := l.complete(system, user, 900)
	if err != nil {
		return nil, "", err
	}
	var parsed struct {
		Guesses   []string `json:"guesses"`
		Reasoning string   `json:"reasoning"`
	}
	if err := jsonUnmarshal(out, &parsed); err != nil {
		return nil, "", fmt.Errorf("parse guess: %w (raw: %s)", err, truncate(out, 120))
	}
	// keep only real board words, normalized to the board's casing
	var clean []string
	for _, g := range parsed.Guesses {
		for _, w := range boardWords {
			if strings.EqualFold(w, strings.TrimSpace(g)) {
				clean = append(clean, w)
				break
			}
		}
	}
	return clean, parsed.Reasoning, nil
}

// jsonUnmarshal parses a possibly fenced/prose-wrapped JSON payload.
func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(extractJSON(s)), v)
}
