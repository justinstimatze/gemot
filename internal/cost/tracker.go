package cost

import (
	"sync"
)

// Tracker accumulates LLM token usage per deliberation.
type Tracker struct {
	mu    sync.Mutex
	costs map[string]*Usage // deliberation ID -> accumulated usage
}

type Usage struct {
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	Calls        int     `json:"calls"`
	EstCostUSD   float64 `json:"est_cost_usd"`
}

func NewTracker() *Tracker {
	return &Tracker{costs: map[string]*Usage{}}
}

// Model pricing: input $/M tokens, output $/M tokens
var modelPricing = map[string][2]float64{
	"claude-sonnet-4-6": {3.0, 15.0},
	"claude-opus-4-6":   {15.0, 75.0},
	"claude-haiku-4-5":  {0.80, 4.0},
}

// Record adds a single LLM call's usage to a deliberation's total.
// Model is used for cost estimation; defaults to Sonnet pricing if unknown.
func (t *Tracker) Record(deliberationID, model string, inputTokens, outputTokens int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	u, ok := t.costs[deliberationID]
	if !ok {
		u = &Usage{}
		t.costs[deliberationID] = u
	}
	u.InputTokens += inputTokens
	u.OutputTokens += outputTokens
	u.TotalTokens += inputTokens + outputTokens
	u.Calls++

	pricing, ok := modelPricing[model]
	if !ok {
		pricing = modelPricing["claude-sonnet-4-6"]
	}
	u.EstCostUSD = float64(u.InputTokens)*pricing[0]/1_000_000 + float64(u.OutputTokens)*pricing[1]/1_000_000
}

// Get returns the accumulated usage for a deliberation.
func (t *Tracker) Get(deliberationID string) *Usage {
	t.mu.Lock()
	defer t.mu.Unlock()

	if u, ok := t.costs[deliberationID]; ok {
		cp := *u
		return &cp
	}
	return &Usage{}
}

// Reset clears usage for a deliberation.
func (t *Tracker) Reset(deliberationID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.costs, deliberationID)
}
