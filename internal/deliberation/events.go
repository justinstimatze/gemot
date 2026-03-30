package deliberation

import (
	"encoding/json"
	"sync"
	"time"
)

// Event represents a state change in a deliberation.
type Event struct {
	Type           string `json:"type"`            // position_submitted, vote_cast, analysis_started, analysis_progress, analysis_complete, deliberation_created
	DeliberationID string `json:"deliberation_id"`
	AgentID        string `json:"agent_id,omitempty"`
	Detail         string `json:"detail,omitempty"` // sub_status for progress, position_id for submissions, etc.
	Timestamp      string `json:"timestamp"`
}

// EventBus provides pub/sub for deliberation events.
// Zero overhead when no subscribers are connected.
type EventBus struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
}

// NewEventBus creates an event bus.
func NewEventBus() *EventBus {
	return &EventBus{
		clients: make(map[chan Event]struct{}),
	}
}

// Subscribe returns a channel that receives events and an unsubscribe function.
func (eb *EventBus) Subscribe(bufSize int) (<-chan Event, func()) {
	ch := make(chan Event, bufSize)
	eb.mu.Lock()
	eb.clients[ch] = struct{}{}
	eb.mu.Unlock()

	return ch, func() {
		eb.mu.Lock()
		delete(eb.clients, ch)
		close(ch)
		eb.mu.Unlock()
	}
}

// Emit sends an event to all subscribers. Non-blocking: drops events for slow clients.
func (eb *EventBus) Emit(e Event) {
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	eb.mu.RLock()
	defer eb.mu.RUnlock()

	// Fast path: no subscribers, no work
	if len(eb.clients) == 0 {
		return
	}

	for ch := range eb.clients {
		select {
		case ch <- e:
		default:
			// slow client, drop
		}
	}
}

// MarshalEvent serializes an event to JSON.
func MarshalEvent(e Event) ([]byte, error) {
	return json.Marshal(e)
}
