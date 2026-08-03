package main

import (
	"fmt"
	"strings"
)

// SummaryArm is the matched control for the structured (gemot) arm. It runs the
// SAME per-seat discussion as the chat arm, then asks a SINGLE LLM to summarise
// the table and recommend which seats to trust — the same "extra synthesis" the
// structured arm appends, but produced by one naive pass instead of gemot's
// crux-detection / voting / compromise pipeline.
//
// This isolates the variable that matters: chat has no synthesis, summary has a
// naive synthesis, structured has gemot's structured synthesis. The summary-vs-
// structured gap is therefore attributable to gemot's aggregation specifically,
// not merely to "the agents got handed a recommendation." Downstream agents see
// an identical "[table synthesis]" line for both arms (Seat -1), so the only
// difference is how that line was produced.
type SummaryArm struct {
	llm     *LLM
	journal *Journal
}

func NewSummaryArm(llm *LLM, j *Journal) *SummaryArm {
	return &SummaryArm{llm: llm, journal: j}
}

func (s *SummaryArm) discuss(g *Game, players []Player, knows []PlayerKnowledge, log []string) []Statement {
	transcript := chatDiscuss(g, players, knows, log) // identical public positions to the chat arm
	if len(transcript) < 2 {
		return transcript
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Avalon quest %d, %d-player game (seats 0..%d). So far %d quests succeeded and %d failed.\n\nThe table just said:\n",
		g.Quest+1, g.NumPlayers, g.NumPlayers-1, g.Successes(), g.Failures())
	for _, st := range transcript {
		fmt.Fprintf(&b, "- seat %d: %s\n", st.Seat, st.Text)
	}
	b.WriteString("\nSome of these players are EVIL and are lying to protect their team. Weigh the statements against the voting and quest evidence, then summarise the table's collective read and recommend which seats are safest (most likely GOOD) to send on the quest. End with a single line in exactly this form:\nRECOMMEND TRUST: seat, seat, ...\nlisting the seats safest to send, most to least trusted.")

	// The system prompt is the same cached prefix used everywhere, so this call
	// reads the warm cache rather than writing a new one.
	out, err := s.llm.complete(avalonSystemPrompt, b.String(), 600)
	if err != nil || strings.TrimSpace(out) == "" {
		// On failure the control is simply the chat arm this round; the missing
		// synthesis entry in the journal makes any such round detectable.
		return transcript
	}
	synth := strings.TrimSpace(out)
	if s.journal != nil {
		s.journal.Record(JournalEntry{Quest: g.Quest + 1, Phase: "summary", Seat: -1,
			Role: "summary", Action: "synthesis", Choice: synth})
	}
	return append(transcript, Statement{Seat: -1, Text: synth})
}
