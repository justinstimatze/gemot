package main

import (
	"fmt"
	"sync"
)

// RunConfig selects an arm's behaviour. Discuss, if set, runs once per quest
// before team selection and produces the public transcript fed into every
// player's view — this is where the gemot-chat and gemot-structured arms inject
// their deliberation. Solo and rule-bot arms leave it nil.
type RunConfig struct {
	Arm     string
	Discuss func(g *Game, players []Player, knows []PlayerKnowledge, log []string) []Statement
	Journal *Journal
}

// RunGame plays one Avalon match to completion under cfg and returns its outcome.
// The loop is identical across arms; only the players' policies and the optional
// discussion step vary, so any win-rate gap is attributable to the arm.
func RunGame(g *Game, players []Player, cfg RunConfig) Outcome {
	knows := make([]PlayerKnowledge, g.NumPlayers)
	for i := range knows {
		knows[i] = g.Knowledge(i)
	}
	for i := range knows {
		cfg.Journal.Record(JournalEntry{Seat: i, Role: g.Roles[i].String(),
			Side: sideName(!g.Roles[i].Evil()), Action: "role"})
	}
	var log []string
	var transcript []Statement
	discussedQuest := -1
	proposals := 0
	guard := 0

	for !g.Done {
		guard++
		if guard > 5000 {
			break // defensive: never spin forever
		}
		switch g.Phase {
		case TeamSelection:
			if cfg.Discuss != nil && discussedQuest != g.Quest {
				transcript = cfg.Discuss(g, players, knows, log)
				discussedQuest = g.Quest
				for _, s := range transcript {
					log = append(log, fmt.Sprintf("(talk) seat %d: %s", s.Seat, truncate(s.Text, 160)))
				}
			}
			leader := g.Leader
			v := viewFor(g, leader, knows, log, nil, transcript)
			team := players[leader].SelectTeam(v)
			if !validTeam(team, g.TeamSize(), g.NumPlayers) {
				team = fallbackTeam(g, leader)
			}
			if err := g.ChooseTeam(leader, team); err != nil {
				_ = g.ChooseTeam(leader, fallbackTeam(g, leader))
			}
			proposals++
			log = append(log, fmt.Sprintf("Quest %d, proposal %d: leader %d proposed team {%s}",
				g.Quest+1, g.Proposal+1, leader, seatList(team)))
		case TeamVoting:
			team := g.Team
			votes := make([]bool, g.NumPlayers)
			var wg sync.WaitGroup
			for i := range players {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					votes[i] = players[i].VoteTeam(viewFor(g, i, knows, log, team, transcript))
				}(i)
			}
			wg.Wait()
			approve := 0
			for _, ok := range votes {
				if ok {
					approve++
				}
			}
			accepted, _ := g.GatherTeamVotes(votes)
			log = append(log, fmt.Sprintf("Team {%s} vote: %d approve / %d reject -> %s",
				seatList(team), approve, g.NumPlayers-approve, acceptedStr(accepted)))
		case QuestVoting:
			team := g.Team
			votes := make([]bool, len(team))
			var wg sync.WaitGroup
			for i, seat := range team {
				wg.Add(1)
				go func(i, seat int) {
					defer wg.Done()
					votes[i] = players[seat].VoteQuest(viewFor(g, seat, knows, log, team, transcript))
				}(i, seat)
			}
			wg.Wait()
			quest := g.Quest
			success, nf, _ := g.GatherQuestVotes(votes)
			log = append(log, fmt.Sprintf("Quest %d resolved: %s (%d fail vote(s))",
				quest+1, successStr(success), nf))
		case Assassination:
			assassin := g.AssassinPlayer()
			v := viewFor(g, assassin, knows, log, nil, transcript)
			target := players[assassin].Assassinate(v)
			if target < 0 || target >= g.NumPlayers {
				target = fallbackAssassin(g, assassin)
			}
			_, _ = g.Assassinate(assassin, target)
			log = append(log, fmt.Sprintf("Assassin (seat %d) targeted seat %d", assassin, target))
		}
	}

	cfg.Journal.Record(JournalEntry{Seat: -1, Action: "result",
		Choice: fmt.Sprintf("goodWin=%v threeSuccesses=%v merlinKilled=%v quests=%v",
			g.GoodVictory, g.Successes() >= questsToWin, g.Successes() >= questsToWin && !g.GoodVictory, g.QuestResults)})
	return Outcome{
		Arm:            cfg.Arm,
		GoodWin:        g.GoodVictory,
		ThreeSuccesses: g.Successes() >= questsToWin,
		MerlinKilled:   g.Successes() >= questsToWin && !g.GoodVictory,
		Proposals:      proposals,
		Results:        append([]bool(nil), g.QuestResults...),
	}
}

func viewFor(g *Game, seat int, knows []PlayerKnowledge, log []string, proposed []int, transcript []Statement) GameView {
	teamSize := 0
	if g.Quest < len(g.preset.teamSizes) {
		teamSize = g.preset.teamSizes[g.Quest] // g.Quest can be past the last quest at assassination
	}
	return GameView{
		Seat:         seat,
		NumPlayers:   g.NumPlayers,
		Know:         knows[seat],
		Quest:        g.Quest,
		Proposal:     g.Proposal,
		Leader:       g.Leader,
		TeamSize:     teamSize,
		QuestSizes:   g.preset.teamSizes[:],
		FailsReq:     g.preset.failsRequired[:],
		Results:      append([]bool(nil), g.QuestResults...),
		ProposedTeam: proposed,
		Log:          log,
		Transcript:   transcript,
	}
}

func fallbackTeam(g *Game, leader int) []int {
	team := []int{leader}
	for p := 0; len(team) < g.TeamSize() && p < g.NumPlayers; p++ {
		if p != leader {
			team = append(team, p)
		}
	}
	return team
}

func fallbackAssassin(g *Game, assassin int) int {
	for p := 0; p < g.NumPlayers; p++ {
		if p != assassin {
			return p
		}
	}
	return assassin
}

func acceptedStr(a bool) string {
	if a {
		return "ACCEPTED"
	}
	return "rejected"
}

func successStr(s bool) string {
	if s {
		return "SUCCESS"
	}
	return "FAIL"
}
