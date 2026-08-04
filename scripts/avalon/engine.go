// Package main implements the Avalon testbed: the conflict-axis probe for gemot.
// Unlike Codenames (cooperative, shared information), Avalon is a hidden-role
// social-deduction game — distributed private information, a good-vs-evil
// interest conflict, deception, and coalition formation. That is the regime where
// gemot's distinctive machinery (reputation, coalition/deception detection,
// structured deliberation) should earn its keep and where a cooperative
// judgment-pooling task cannot show it.
//
// engine.go is a clean-room Go port of the Avalon rules state machine. The
// authoritative reference for the phase logic and the quest/role preset tables is
// AvalonBench (Li et al., "AvalonBench: Evaluating LLMs Playing the Game of
// Avalon", arXiv:2310.05036; avalonbench_dev/avalon/engine.py) — game rules are
// not copyrightable, and porting keeps the whole harness native Go (no unlicensed
// Python dependency, no cross-language shim) so the win-rate metric stays
// comparable to AvalonBench's published baselines.
//
// Deliberate improvement over the reference: AvalonBench's get_partial_sides
// gives Merlin and all evil full visibility of every side, so it does NOT hide
// Mordred from Merlin or Oberon from the other evil. Those are exactly the
// deception-relevant roles, so Knowledge() below implements correct Avalon night
// information while the scoring state machine remains bit-for-bit comparable.
package main

import (
	"errors"
	"fmt"
	"math/rand"
)

// Role is a player's secret identity. IDs match AvalonBench's ROLES map.
type Role int

const (
	Merlin   Role = iota // 0 — good; sees evil except Mordred
	Percival             // 1 — good; sees Merlin and Morgana (indistinguishable)
	Morgana              // 2 — evil; appears as Merlin to Percival
	Mordred              // 3 — evil; hidden from Merlin
	Oberon               // 4 — evil; hidden from other evil and sees no evil
	Servant              // 5 — good; no special knowledge
	Minion               // 6 — evil; plain evil
	Assassin             // 7 — evil; assassinates at the end if good win the quests
)

var roleNames = map[Role]string{
	Merlin: "Merlin", Percival: "Percival", Morgana: "Morgana", Mordred: "Mordred",
	Oberon: "Oberon", Servant: "Servant", Minion: "Minion", Assassin: "Assassin",
}

func (r Role) String() string {
	if n, ok := roleNames[r]; ok {
		return n
	}
	return fmt.Sprintf("Role(%d)", int(r))
}

// Evil reports whether the role is on the evil team.
func (r Role) Evil() bool {
	switch r {
	case Morgana, Mordred, Oberon, Minion, Assassin:
		return true
	default:
		return false
	}
}

// Phase is the current step of a quest cycle. IDs match AvalonBench's PHASES map.
type Phase int

const (
	TeamSelection Phase = iota // leader proposes a quest team
	TeamVoting                 // all players approve/reject the proposal
	QuestVoting                // team members secretly pass/fail the quest
	Assassination              // after 3 successes, the assassin guesses Merlin
)

var phaseNames = map[Phase]string{
	TeamSelection: "Team Selection", TeamVoting: "Team Voting",
	QuestVoting: "Quest Voting", Assassination: "Assassination",
}

func (p Phase) String() string { return phaseNames[p] }

// preset captures a player count's fixed configuration:
// team side split, the five quest team sizes, and fails-required per quest.
type preset struct {
	numGood, numEvil int
	teamSizes        [5]int
	failsRequired    [5]int
}

// questPresets mirrors AvalonBench's QUEST_PRESET (5–10 players).
var questPresets = map[int]preset{
	5:  {3, 2, [5]int{2, 3, 2, 3, 3}, [5]int{1, 1, 1, 1, 1}},
	6:  {4, 2, [5]int{2, 3, 4, 3, 4}, [5]int{1, 1, 1, 1, 1}},
	7:  {4, 3, [5]int{2, 3, 3, 4, 4}, [5]int{1, 1, 1, 2, 1}},
	8:  {5, 3, [5]int{3, 4, 4, 5, 5}, [5]int{1, 1, 1, 2, 1}},
	9:  {6, 3, [5]int{3, 4, 4, 5, 5}, [5]int{1, 1, 1, 2, 1}},
	10: {6, 4, [5]int{3, 4, 4, 5, 5}, [5]int{1, 1, 1, 2, 1}},
}

// maxProposals is the number of team proposals allowed per quest before the fifth
// proposal passes automatically regardless of votes (the "hammer").
const maxProposals = 5

// questsToWin is how many quest successes (good) or failures (evil) end the game.
const questsToWin = 3

// Options toggles the optional special roles. Merlin and the Assassin are always
// present; the rest of each side is filled with Servants / Minions.
type Options struct {
	Percival bool
	Morgana  bool
	Mordred  bool
	Oberon   bool
}

var errGameEnded = errors.New("game has ended")

// Game is one Avalon match: fixed configuration plus mutable play state.
type Game struct {
	rng        *rand.Rand
	NumPlayers int
	preset     preset
	opts       Options

	Roles []Role // secret role per player

	Phase        Phase
	Quest        int    // index of the current quest, 0..4 (AvalonBench "turn")
	Proposal     int    // rejected proposals so far this quest, 0..4 (AvalonBench "round")
	Leader       int    // current quest leader
	Team         []int  // players on the currently proposed/accepted team
	QuestResults []bool // one entry per resolved quest: true = success
	Done         bool
	GoodVictory  bool
}

// NewGame builds and initialises a game for numPlayers with the given optional
// roles, drawing all randomness from rng (nil-safe callers should pass their own
// seeded source for reproducibility).
func NewGame(numPlayers int, opts Options, rng *rand.Rand) (*Game, error) {
	p, ok := questPresets[numPlayers]
	if !ok {
		return nil, fmt.Errorf("unsupported player count %d (must be 5-10)", numPlayers)
	}
	forcedEvil := 1 // Assassin
	if opts.Morgana {
		forcedEvil++
	}
	if opts.Mordred {
		forcedEvil++
	}
	if opts.Oberon {
		forcedEvil++
	}
	if forcedEvil > p.numEvil {
		return nil, fmt.Errorf("%d special evil roles requested but only %d evil seats at %d players", forcedEvil, p.numEvil, numPlayers)
	}
	g := &Game{rng: rng, NumPlayers: numPlayers, preset: p, opts: opts}
	g.reset()
	return g, nil
}

func (g *Game) reset() {
	g.Phase = TeamSelection
	g.Quest = 0
	g.Proposal = 0
	g.Done = false
	g.GoodVictory = false
	g.QuestResults = nil
	g.Team = nil
	g.Leader = g.rng.Intn(g.NumPlayers)
	g.assignRoles()
}

// assignRoles deals secret roles: numEvil evil seats filled with Assassin + any
// enabled special evil + Minions, numGood good seats with Merlin + optional
// Percival + Servants, each side shuffled independently.
func (g *Game) assignRoles() {
	roles := make([]Role, g.NumPlayers)
	perm := g.rng.Perm(g.NumPlayers)
	evilSeats := perm[:g.preset.numEvil]
	goodSeats := perm[g.preset.numEvil:]

	evilRoles := []Role{Assassin}
	if g.opts.Morgana {
		evilRoles = append(evilRoles, Morgana)
	}
	if g.opts.Mordred {
		evilRoles = append(evilRoles, Mordred)
	}
	if g.opts.Oberon {
		evilRoles = append(evilRoles, Oberon)
	}
	for len(evilRoles) < g.preset.numEvil {
		evilRoles = append(evilRoles, Minion)
	}
	g.rng.Shuffle(len(evilRoles), func(i, j int) { evilRoles[i], evilRoles[j] = evilRoles[j], evilRoles[i] })
	for i, seat := range evilSeats {
		roles[seat] = evilRoles[i]
	}

	goodRoles := []Role{Merlin}
	if g.opts.Percival {
		goodRoles = append(goodRoles, Percival)
	}
	for len(goodRoles) < g.preset.numGood {
		goodRoles = append(goodRoles, Servant)
	}
	g.rng.Shuffle(len(goodRoles), func(i, j int) { goodRoles[i], goodRoles[j] = goodRoles[j], goodRoles[i] })
	for i, seat := range goodSeats {
		roles[seat] = goodRoles[i]
	}

	g.Roles = roles
}

// IsGood reports whether a player is on the good team.
func (g *Game) IsGood(player int) bool { return !g.Roles[player].Evil() }

// TeamSize is the number of players required on the current quest's team.
func (g *Game) TeamSize() int { return g.preset.teamSizes[g.Quest] }

// FailsRequired is how many fail votes sink the current quest.
func (g *Game) FailsRequired() int { return g.preset.failsRequired[g.Quest] }

// Successes / Failures count resolved quests by outcome.
func (g *Game) Successes() int { return countBool(g.QuestResults, true) }
func (g *Game) Failures() int  { return countBool(g.QuestResults, false) }

func countBool(bs []bool, want bool) int {
	n := 0
	for _, b := range bs {
		if b == want {
			n++
		}
	}
	return n
}

// AssassinPlayer returns the seat holding the Assassin role, or -1 if none.
func (g *Game) AssassinPlayer() int {
	for p, r := range g.Roles {
		if r == Assassin {
			return p
		}
	}
	return -1
}

// ChooseTeam records the leader's proposed team and advances to team voting.
func (g *Game) ChooseTeam(leader int, team []int) error {
	if g.Done {
		return errGameEnded
	}
	if g.Phase != TeamSelection {
		return fmt.Errorf("cannot choose team in phase %s", g.Phase)
	}
	if leader != g.Leader {
		return fmt.Errorf("player %d is not the quest leader (%d)", leader, g.Leader)
	}
	if len(team) != g.TeamSize() {
		return fmt.Errorf("team size %d != required %d", len(team), g.TeamSize())
	}
	seen := make(map[int]bool, len(team))
	for _, p := range team {
		if p < 0 || p >= g.NumPlayers {
			return fmt.Errorf("player %d out of range", p)
		}
		if seen[p] {
			return fmt.Errorf("duplicate player %d on team", p)
		}
		seen[p] = true
	}
	g.Team = append([]int(nil), team...)
	g.Phase = TeamVoting
	g.Leader = (g.Leader + 1) % g.NumPlayers // leadership passes on proposal, per AvalonBench
	return nil
}

// GatherTeamVotes tallies approve/reject votes (one per player, indexed by seat).
// The fifth proposal of a quest passes automatically. Returns whether the team
// was accepted; on rejection play returns to team selection with the next leader.
func (g *Game) GatherTeamVotes(votes []bool) (accepted bool, err error) {
	if g.Done {
		return false, errGameEnded
	}
	if g.Phase != TeamVoting {
		return false, fmt.Errorf("cannot vote on team in phase %s", g.Phase)
	}
	if len(votes) != g.NumPlayers {
		return false, fmt.Errorf("expected %d team votes, got %d", g.NumPlayers, len(votes))
	}
	if g.Proposal == maxProposals-1 { // hammer: fifth proposal auto-passes
		g.Phase = QuestVoting
		g.Proposal = 0
		return true, nil
	}
	approve := countBool(votes, true)
	if approve*2 > g.NumPlayers { // strict majority
		g.Phase = QuestVoting
		g.Proposal = 0
		return true, nil
	}
	g.Phase = TeamSelection
	g.Proposal++
	return false, nil
}

// GatherQuestVotes tallies the team's secret pass/fail votes (one per team
// member). Returns whether the quest succeeded and the number of fail votes, and
// advances to the next quest, the assassination phase, or an evil victory.
func (g *Game) GatherQuestVotes(votes []bool) (success bool, numFails int, err error) {
	if g.Done {
		return false, 0, errGameEnded
	}
	if g.Phase != QuestVoting {
		return false, 0, fmt.Errorf("cannot vote on quest in phase %s", g.Phase)
	}
	if len(votes) != g.TeamSize() {
		return false, 0, fmt.Errorf("expected %d quest votes, got %d", g.TeamSize(), len(votes))
	}
	numFails = countBool(votes, false)
	if numFails >= g.FailsRequired() {
		g.QuestResults = append(g.QuestResults, false)
		g.Quest++
		if g.Failures() == questsToWin {
			g.Done = true
			g.GoodVictory = false
		} else {
			g.Phase = TeamSelection
		}
		return false, numFails, nil
	}
	g.QuestResults = append(g.QuestResults, true)
	g.Quest++
	if g.Successes() == questsToWin {
		g.Phase = Assassination
	} else {
		g.Phase = TeamSelection
	}
	return true, numFails, nil
}

// Assassinate resolves the final phase: if the assassin names Merlin, evil win
// despite three successful quests; otherwise good win. Ends the game.
func (g *Game) Assassinate(assassin, target int) (goodWins bool, err error) {
	if g.Done {
		return false, errGameEnded
	}
	if g.Phase != Assassination {
		return false, fmt.Errorf("cannot assassinate in phase %s", g.Phase)
	}
	if assassin < 0 || assassin >= g.NumPlayers || g.Roles[assassin] != Assassin {
		return false, fmt.Errorf("player %d is not the assassin", assassin)
	}
	if target < 0 || target >= g.NumPlayers {
		return false, fmt.Errorf("target %d out of range", target)
	}
	g.Done = true
	g.GoodVictory = g.Roles[target] != Merlin && g.Successes() >= questsToWin
	return g.GoodVictory, nil
}

// PlayerKnowledge is what a player learns from their night information.
type PlayerKnowledge struct {
	Role Role
	Good bool
	// SeenEvil are players this player knows to be evil: for Merlin, all evil
	// except Mordred; for evil players other than Oberon, their fellow evil
	// except Oberon; empty for everyone else.
	SeenEvil []int
	// MerlinAndMorgana are the two players who appear magical to Percival (the
	// real Merlin and Morgana, indistinguishable and shuffled); nil otherwise.
	MerlinAndMorgana []int
}

// Knowledge returns a player's correct Avalon night information — the private,
// distributed knowledge that makes the game a hidden-role problem.
func (g *Game) Knowledge(player int) PlayerKnowledge {
	role := g.Roles[player]
	pk := PlayerKnowledge{Role: role, Good: !role.Evil()}
	switch {
	case role == Merlin:
		for p, r := range g.Roles {
			if r.Evil() && r != Mordred {
				pk.SeenEvil = append(pk.SeenEvil, p)
			}
		}
	case role.Evil() && role != Oberon:
		for p, r := range g.Roles {
			if p != player && r.Evil() && r != Oberon {
				pk.SeenEvil = append(pk.SeenEvil, p)
			}
		}
	}
	if role == Percival {
		for p, r := range g.Roles {
			if r == Merlin || r == Morgana {
				pk.MerlinAndMorgana = append(pk.MerlinAndMorgana, p)
			}
		}
		g.rng.Shuffle(len(pk.MerlinAndMorgana), func(i, j int) {
			pk.MerlinAndMorgana[i], pk.MerlinAndMorgana[j] = pk.MerlinAndMorgana[j], pk.MerlinAndMorgana[i]
		})
	}
	return pk
}
