package main

import (
	"fmt"
	"sort"
	"strings"
)

// Player is one seat's decision policy. Rule-bots and LLM agents implement it, so
// the game runner is identical across arms and only the policy varies.
type Player interface {
	Name() string
	// SelectTeam is called when this player is the quest leader; it returns
	// exactly v.TeamSize distinct seat indices for the proposed quest team.
	SelectTeam(v GameView) []int
	// VoteTeam approves (true) or rejects (false) the proposed team in v.ProposedTeam.
	VoteTeam(v GameView) bool
	// VoteQuest is called for a player on the quest team: pass (true) or fail (false).
	VoteQuest(v GameView) bool
	// Assassinate is called for the Assassin after good pass 3 quests; returns the
	// seat it believes is Merlin.
	Assassinate(v GameView) int
	// Discuss returns this player's public statement for a discussion round, or ""
	// if the arm has no discussion. Evil players are expected to deceive.
	Discuss(v GameView) string
}

// Statement is one public utterance in a discussion round.
type Statement struct {
	Seat int
	Text string
}

// GameView is everything a player may see when making a decision: public game
// state plus that player's own private night knowledge. It never leaks other
// players' secret roles.
type GameView struct {
	Seat       int
	NumPlayers int
	Know       PlayerKnowledge

	Quest      int   // current quest index (0-based)
	Proposal   int   // rejected proposals so far this quest
	Leader     int   // current leader seat
	TeamSize   int   // required team size for this quest
	QuestSizes []int // team size per quest
	FailsReq   []int // fails required to sink each quest

	Results      []bool      // resolved quest outcomes (true = success)
	ProposedTeam []int       // team under consideration (for VoteTeam / VoteQuest context)
	Log          []string    // public event log, human-readable
	Transcript   []Statement // discussion statements this round (chat/structured arms)
}

// score summary of quests so far.
func (v GameView) scoreLine() string {
	s, f := 0, 0
	for _, r := range v.Results {
		if r {
			s++
		} else {
			f++
		}
	}
	return fmt.Sprintf("Quests: %d succeeded, %d failed (good needs 3 to win, evil needs 3).", s, f)
}

// knowledgeLine renders this player's private night information for its prompt.
func (v GameView) knowledgeLine() string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are seat %d. Your secret role is %s (%s).",
		v.Seat, v.Know.Role, sideName(v.Know.Good))
	if len(v.Know.SeenEvil) > 0 {
		fmt.Fprintf(&b, " You know these seats are EVIL: %s.", seatList(v.Know.SeenEvil))
	}
	if len(v.Know.MerlinAndMorgana) > 0 {
		fmt.Fprintf(&b, " One of these seats is Merlin, the other is Morgana (you can't tell which): %s.", seatList(v.Know.MerlinAndMorgana))
	}
	switch {
	case v.Know.Role == Merlin:
		b.WriteString(" You must guide good WITHOUT revealing you are Merlin — if evil identify you, the Assassin can kill you and win.")
	case v.Know.Role == Assassin:
		b.WriteString(" If good pass 3 quests, you get one guess at Merlin's identity to steal the win.")
	case v.Know.Good:
		b.WriteString(" You have no special knowledge; deduce who is evil from proposals and votes.")
	default:
		b.WriteString(" Sabotage quests and blend in; do not reveal you are evil.")
	}
	return b.String()
}

// promptContext renders the shared public situation for any decision prompt.
func (v GameView) promptContext() string {
	var b strings.Builder
	b.WriteString(gameRules)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "This is a %d-player game. %s\n", v.NumPlayers, v.knowledgeLine())
	fmt.Fprintf(&b, "%s\n", v.scoreLine())
	if v.Quest < len(v.FailsReq) {
		fmt.Fprintf(&b, "Current quest %d needs a team of %d; %d fail vote(s) sink it. Leader is seat %d. This is proposal %d of 5 this quest.\n",
			v.Quest+1, v.TeamSize, v.FailsReq[v.Quest], v.Leader, v.Proposal+1)
	}
	if len(v.Log) > 0 {
		b.WriteString("\nPublic history:\n")
		for _, e := range v.Log {
			fmt.Fprintf(&b, "  - %s\n", e)
		}
	}
	if len(v.Transcript) > 0 {
		b.WriteString("\nThis round's table talk:\n")
		for _, s := range v.Transcript {
			if s.Seat < 0 {
				fmt.Fprintf(&b, "  - [table synthesis]: %s\n", s.Text)
			} else {
				fmt.Fprintf(&b, "  - seat %d: %s\n", s.Seat, s.Text)
			}
		}
	}
	return b.String()
}

func sideName(good bool) string {
	if good {
		return "GOOD"
	}
	return "EVIL"
}

func seatList(seats []int) string {
	ss := append([]int(nil), seats...)
	sort.Ints(ss)
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = fmt.Sprintf("%d", s)
	}
	return strings.Join(parts, ", ")
}

// validTeam checks a proposed team is the right size, in range, and distinct.
func validTeam(team []int, size, n int) bool {
	if len(team) != size {
		return false
	}
	seen := make(map[int]bool, len(team))
	for _, p := range team {
		if p < 0 || p >= n || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

// Outcome records one finished game for metric aggregation.
type Outcome struct {
	Arm            string
	GoodWin        bool
	ThreeSuccesses bool // good passed 3 quests (reached assassination)
	MerlinKilled   bool // assassin correctly named Merlin
	Proposals      int  // total team proposals across the game
	Results        []bool
}
