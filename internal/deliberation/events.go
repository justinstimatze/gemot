package deliberation

import (
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// ErrTooManyClients is returned when SubscribeIfUnder exceeds the limit.
var ErrTooManyClients = errors.New("too many event stream clients")

// Event represents a state change in a deliberation.
type Event struct {
	Type           string `json:"type"` // position_submitted, vote_cast, analysis_started, analysis_progress, analysis_complete, deliberation_created
	DeliberationID string `json:"deliberation_id"`
	AgentID        string `json:"agent_id,omitempty"`
	Detail         string `json:"detail,omitempty"` // sub_status for progress, position_id for submissions, etc.
	Timestamp      string `json:"timestamp"`
	Data           any    `json:"data,omitempty"` // optional rich payload (position content, vote value, etc.)
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

	return ch, func() { eb.closeAndRemove(ch) }
}

// SubscribeIfUnder atomically checks the client count and subscribes in one
// lock acquisition, eliminating the TOCTOU race between ClientCount and Subscribe.
func (eb *EventBus) SubscribeIfUnder(limit, bufSize int) (<-chan Event, func(), error) {
	ch := make(chan Event, bufSize)
	eb.mu.Lock()
	if len(eb.clients) >= limit {
		eb.mu.Unlock()
		return nil, nil, ErrTooManyClients
	}
	eb.clients[ch] = struct{}{}
	eb.mu.Unlock()

	return ch, func() { eb.closeAndRemove(ch) }, nil
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

// ClientCount returns the number of active subscribers.
func (eb *EventBus) ClientCount() int {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return len(eb.clients)
}

// Shutdown sends a shutdown event to all clients and closes their channels.
// Clients reading from the channel will receive the event then see channel
// close. Closing happens under the same lock a subscriber's own
// closeAndRemove uses, and only for channels still present in the client
// set -- see closeAndRemove's doc comment for why that matters.
func (eb *EventBus) Shutdown() {
	shutdownEvent := Event{
		Type:      "server_shutdown",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	eb.mu.Lock()
	defer eb.mu.Unlock()
	for ch := range eb.clients {
		select {
		case ch <- shutdownEvent:
		default:
		}
		close(ch)
		delete(eb.clients, ch)
	}
}

// MarshalEvent serializes an event to JSON.
func MarshalEvent(e Event) ([]byte, error) {
	return json.Marshal(e)
}

// closeAndRemove closes ch and removes it from the client set, but only if
// it is still present. Shutdown and a subscriber's own unsubscribe function
// (typically deferred in an SSE handler) can race to close the same
// channel -- e.g. a graceful server shutdown fires while a request's defer
// also runs -- and closing an already-closed channel panics. Both paths
// funnel through here (or, for Shutdown, an equivalent inline check under
// the same lock) so exactly one of them wins; the loser's delete is then a
// safe no-op.
func (eb *EventBus) closeAndRemove(ch chan Event) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	if _, ok := eb.clients[ch]; !ok {
		return
	}
	delete(eb.clients, ch)
	close(ch)
}
