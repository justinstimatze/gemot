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
	var avoidNote string // feedback appended after a clue that flunks the danger check
	for try := 0; try < 4; try++ {
		prompt := user
		if avoidNote != "" {
			prompt += "\n\n" + avoidNote
		}
		out, cerr := l.complete("You are an expert Codenames spymaster. Every target must be one of your team's words. Output only the JSON object.", prompt, 2048)
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
		fullyValid := cl != "" && validTargets == len(parsed.Targets) && validTargets > 0
		if !fullyValid {
			if validTargets > len(best.idx) {
				best = attempt{cl, teamIdx}
			}
			continue
		}
		// Danger check: a clue whose targets are all team words can still walk the
		// guessers into the assassin (the "STATION" -> SPACE wipe). Simulate a
		// guesser's read of the clue against the real board; if the assassin
		// surfaces within the words a guesser would reach, reject and retry with
		// explicit feedback rather than shipping a losing clue.
		if len(assassin) > 0 {
			if ranked, rerr := l.clueRanking(cl, b.Words); rerr == nil {
				if rank := assassinRank(ranked, assassin[0]); rank >= 0 && rank <= validTargets {
					avoidNote = fmt.Sprintf("Your previous clue %q pulls too strongly toward the ASSASSIN word %q -- a guesser reading it would reach %q. Choose a DIFFERENT clue that does not evoke %q at all.", cl, assassin[0], assassin[0], assassin[0])
					if len(best.idx) == 0 { // keep only as an absolute last resort
						best = attempt{cl, teamIdx}
					}
					continue
				}
			}
		}
		return cl, validTargets, teamIdx, nil // valid AND not assassin-adjacent
	}
	if len(best.idx) == 0 {
		if err == nil {
			err = fmt.Errorf("codemaster produced no valid team targets after retries")
		}
		return "", 0, nil, err
	}
	return best.clue, len(best.idx), best.idx, nil
}

// clueRanking asks the model to rank board words by association strength to a
// clue, exactly as a guesser would read it. Used to detect clues that lead into
// the assassin before the clue is ever shown to the guessing fleet.
func (l *LLM) clueRanking(clue string, boardWords []string) ([]string, error) {
	user := fmt.Sprintf(`Board words: %s

For the one-word clue %q, list the board words most strongly associated with it, from MOST to LEAST associated. Include the top 8. Use only words from the board.

Reply with JSON only:
{"ranked": ["<word>", ...]}`, strings.Join(boardWords, ", "), clue)
	out, err := l.complete("You rank word associations like a Codenames guesser. Output only the JSON object.", user, 400)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Ranked []string `json:"ranked"`
	}
	if err := jsonUnmarshal(out, &parsed); err != nil {
		return nil, err
	}
	return parsed.Ranked, nil
}

// assassinRank is the 0-based position of the assassin word in a ranked list, or
// -1 if it does not appear.
func assassinRank(ranked []string, assassin string) int {
	for i, w := range ranked {
		if strings.EqualFold(strings.TrimSpace(w), assassin) {
			return i
		}
	}
	return -1
}
