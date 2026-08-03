package main

import (
	"encoding/json"
	"os"
	"sync"
)

// JournalEntry is one recorded event in a game. Private holds an agent's PRIVATE
// reasoning — its scratchpad / plan, never shown to other players — so a finished
// run can be audited: e.g. compare an evil agent's stated deception plan against
// what actually happened. Choice holds the public/observable decision.
type JournalEntry struct {
	Arm     string `json:"arm"`
	Game    int    `json:"game"`
	Quest   int    `json:"quest,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Seat    int    `json:"seat"`
	Role    string `json:"role,omitempty"`
	Side    string `json:"side,omitempty"`
	Action  string `json:"action"`
	Choice  string `json:"choice,omitempty"`
	Private string `json:"private,omitempty"`
}

// Journal accumulates entries across a run and writes them as JSONL. Begin stamps
// the arm/game onto every subsequent entry so callers pass only local details.
type Journal struct {
	mu      sync.Mutex
	entries []JournalEntry
	arm     string
	game    int
}

func NewJournal() *Journal { return &Journal{} }

func (j *Journal) Begin(arm string, game int) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.arm, j.game = arm, game
	j.mu.Unlock()
}

func (j *Journal) Record(e JournalEntry) {
	if j == nil {
		return
	}
	j.mu.Lock()
	e.Arm, e.Game = j.arm, j.game
	j.entries = append(j.entries, e)
	j.mu.Unlock()
}

// Truncate drops entries back to length n. Used to discard a seed whose
// structured arm degraded, so a contaminated game never reaches the journal.
func (j *Journal) Truncate(n int) {
	if j == nil {
		return
	}
	j.mu.Lock()
	if n >= 0 && n < len(j.entries) {
		j.entries = j.entries[:n]
	}
	j.mu.Unlock()
}

func (j *Journal) Len() int {
	if j == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.entries)
}

// WriteJSONL writes one entry per line, chronological order.
func (j *Journal) WriteJSONL(path string) error {
	if j == nil {
		return nil
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	enc := json.NewEncoder(f)
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range j.entries {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	return nil
}
