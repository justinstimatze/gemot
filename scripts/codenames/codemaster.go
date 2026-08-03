package main

import (
	"fmt"
	"strings"
)

// Clue is the spymaster move: given the full board and key, produce a one-word
// clue, a number, and the team words it targets. The codemaster is honest (it
// sees the key); the guessers do not — so guessing is genuine inference.
func (l *LLM) Clue(b Board) (clue string, n int, intended []int, err error) {
	var team, opp, civ, assassin []string
	for i, w := range b.Words {
		switch b.Key[i] {
		case Team:
			team = append(team, w)
		case Opponent:
			opp = append(opp, w)
		case Assassin:
			assassin = append(assassin, w)
		default:
			civ = append(civ, w)
		}
	}
	user := fmt.Sprintf(`%s

You are the spymaster. Your team's words: %s
Opponent's words: %s
Civilians: %s
Assassin (NEVER lead the team here): %s

Give ONE single-word clue (not any word on the board) and a number N of your team's words it points to. Pick a clue your team can decode but that does not pull toward the assassin or opponent words.

Reply with JSON only:
{"clue": "<one word>", "number": <int>, "targets": ["<team word>", ...]}`,
		gameRules, strings.Join(team, ", "), strings.Join(opp, ", "),
		strings.Join(civ, ", "), strings.Join(assassin, ", "))

	out, err := l.complete("You are an expert Codenames spymaster. Respond with only the JSON object requested.", user, 1024)
	if err != nil {
		return "", 0, nil, err
	}
	var parsed struct {
		Clue    string   `json:"clue"`
		Number  int      `json:"number"`
		Targets []string `json:"targets"`
	}
	if err := jsonUnmarshal(out, &parsed); err != nil {
		return "", 0, nil, fmt.Errorf("parse clue: %w (raw: %s)", err, truncate(out, 120))
	}
	// map targets -> board indices
	for _, t := range parsed.Targets {
		for i, w := range b.Words {
			if strings.EqualFold(w, t) {
				intended = append(intended, i)
			}
		}
	}
	return strings.TrimSpace(parsed.Clue), parsed.Number, intended, nil
}
