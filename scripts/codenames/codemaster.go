package main

import (
	"fmt"
	"strings"
)

// Clue is the spymaster move: given the full board and key, produce a one-word
// clue, a number, and the team words it targets. The codemaster is honest (it
// sees the key). Because a weak model sometimes clues toward NON-team words, we
// validate against the key: retry until every target is a team word, and on the
// final attempt filter to the team subset so the returned intent is always
// valid (a bad clue can only lose points for the guessers, never the oracle).
func (l *LLM) Clue(b Board) (clue string, n int, intended []int, err error) {
	var team, opp, civ, assassin []string
	teamSet := map[string]bool{}
	for i, w := range b.Words {
		switch b.Key[i] {
		case Team:
			team = append(team, w)
			teamSet[strings.ToUpper(w)] = true
		case Opponent:
			opp = append(opp, w)
		case Assassin:
			assassin = append(assassin, w)
		default:
			civ = append(civ, w)
		}
	}
	user := fmt.Sprintf(`%s

You are the spymaster. Your team's words (clue ONLY toward these): %s
Opponent's words (avoid): %s
Civilians (avoid): %s
Assassin (NEVER point here): %s

Give ONE single-word clue (not any word on the board) and the number N of YOUR TEAM'S words it points to. Every word in "targets" MUST be one of your team's words listed above. Choose a clue your team can decode that does not pull toward the assassin, opponent, or civilian words.

Reply with JSON only:
{"clue": "<one word>", "number": <int>, "targets": ["<team word>", ...]}`,
		gameRules, strings.Join(team, ", "), strings.Join(opp, ", "),
		strings.Join(civ, ", "), strings.Join(assassin, ", "))

	type attempt struct {
		clue string
		idx  []int
	}
	var best attempt
	for try := 0; try < 3; try++ {
		out, cerr := l.complete("You are an expert Codenames spymaster. Every target must be one of your team's words. Output only the JSON object.", user, 2048)
		if cerr != nil {
			return "", 0, nil, cerr
		}
		var parsed struct {
			Clue    string   `json:"clue"`
			Targets []string `json:"targets"`
		}
		if perr := jsonUnmarshal(out, &parsed); perr != nil {
			err = fmt.Errorf("parse clue: %w (raw: %s)", perr, truncate(out, 120))
			continue
		}
		// keep only targets that are genuinely team words
		var teamIdx []int
		var validTargets int
		for _, t := range parsed.Targets {
			if !teamSet[strings.ToUpper(strings.TrimSpace(t))] {
				continue
			}
			for i, w := range b.Words {
				if strings.EqualFold(w, t) && b.Key[i] == Team {
					teamIdx = append(teamIdx, i)
				}
			}
		}
		validTargets = len(teamIdx)
		cl := strings.TrimSpace(parsed.Clue)
		if cl != "" && validTargets == len(parsed.Targets) && validTargets > 0 {
			return cl, validTargets, teamIdx, nil // fully valid clue
		}
		if validTargets > len(best.idx) {
			best = attempt{cl, teamIdx}
		}
	}
	if len(best.idx) == 0 {
		if err == nil {
			err = fmt.Errorf("codemaster produced no valid team targets after retries")
		}
		return "", 0, nil, err
	}
	return best.clue, len(best.idx), best.idx, nil
}
