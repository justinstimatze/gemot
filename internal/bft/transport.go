package bft

import (
	"fmt"
	"sync"
)

// Message is the union of all protocol message types. Session 1 uses
// pointer-nil-or-set discrimination; session 2 may switch to a
// gob/proto-encoded wire format once HTTPTransport lands.
type Message struct {
	Proposal *Proposal
	Vote     *Vote
	NewView  *NewView
}

// Transport abstracts replica-to-replica message delivery. Session 1
// only has InMemoryTransport (channel-based, for tests). Session 2+
// adds an HTTPTransport for multi-node deployment.
type Transport interface {
	// Send delivers msg to the replica identified by `to`.
	Send(to ReplicaID, msg Message) error
	// Broadcast delivers msg to every replica in the configured roster,
	// including the sender (idempotent — receiver may dedupe).
	Broadcast(msg Message) error
	// Recv returns a channel on which incoming messages arrive. The
	// channel is closed when the transport shuts down.
	Recv() <-chan Message
}

// InMemoryTransport wires a set of replicas together via Go channels.
// Constructed via NewInMemoryNetwork, which gives each replica its own
// Transport instance backed by a shared peer map.
type InMemoryTransport struct {
	self  ReplicaID
	peers map[ReplicaID]chan Message
	inbox chan Message
}

// Send delivers to the peer's inbox channel. Blocks if the inbox is
// full; session 1 uses an unbounded buffer so this never blocks in
// practice.
func (t *InMemoryTransport) Send(to ReplicaID, msg Message) error {
	peer, ok := t.peers[to]
	if !ok {
		return fmt.Errorf("bft: no peer %d in transport", to)
	}
	peer <- msg
	return nil
}

// Broadcast sends to every peer in the roster (including self).
func (t *InMemoryTransport) Broadcast(msg Message) error {
	for _, ch := range t.peers {
		ch <- msg
	}
	return nil
}

// Recv returns this replica's inbox channel.
func (t *InMemoryTransport) Recv() <-chan Message { return t.inbox }

// NewInMemoryNetwork creates a Transport for each replica in the
// roster, with all inboxes sized to bufSize (pass 0 for unbuffered —
// use a generous buffer in tests to avoid the Send-blocks-Receive
// deadlock pattern). Returns a map keyed by ReplicaID.
func NewInMemoryNetwork(roster []ReplicaID, bufSize int) map[ReplicaID]*InMemoryTransport {
	inboxes := make(map[ReplicaID]chan Message, len(roster))
	for _, id := range roster {
		inboxes[id] = make(chan Message, bufSize)
	}
	result := make(map[ReplicaID]*InMemoryTransport, len(roster))
	for _, id := range roster {
		// Each transport gets the same peer map (shared).
		peers := make(map[ReplicaID]chan Message, len(roster))
		for pid, ch := range inboxes {
			peers[pid] = ch
		}
		result[id] = &InMemoryTransport{
			self:  id,
			peers: peers,
			inbox: inboxes[id],
		}
	}
	return result
}

// HTTPTransport is a session-2+ placeholder. The design doc
// (specs/hotstuff-design.md) tracks the binding to Fly multi-machine
// deployment. Kept as a declared type so session-1 tests can assert
// the Transport interface is extensible without breaking API changes.
type HTTPTransport struct {
	self   ReplicaID
	roster []ReplicaID
	mu     sync.Mutex
}

// Send is unimplemented in session 1. Calling it panics — this is the
// loud-failure contract so accidental reuse of this type outside tests
// is caught immediately.
func (t *HTTPTransport) Send(to ReplicaID, msg Message) error {
	return fmt.Errorf("bft: HTTPTransport.Send unimplemented (session 2 work — see specs/hotstuff-design.md)")
}

// Broadcast is unimplemented in session 1.
func (t *HTTPTransport) Broadcast(msg Message) error {
	return fmt.Errorf("bft: HTTPTransport.Broadcast unimplemented (session 2 work)")
}

// Recv is unimplemented in session 1.
func (t *HTTPTransport) Recv() <-chan Message {
	ch := make(chan Message)
	close(ch)
	return ch
}
