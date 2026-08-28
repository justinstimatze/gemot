package deliberation

import (
	"sync"
	"testing"
)

// TestEventBusShutdownClosesAllChannels confirms Shutdown still closes every
// subscriber's channel and delivers the shutdown event when there is no
// racing unsub -- the ordinary shutdown path.
func TestEventBusShutdownClosesAllChannels(t *testing.T) {
	eb := NewEventBus()
	ch, _, err := eb.SubscribeIfUnder(10, 4)
	if err != nil {
		t.Fatalf("SubscribeIfUnder: %v", err)
	}
	eb.Shutdown()

	event, ok := <-ch
	if !ok {
		t.Fatal("expected the shutdown event before the channel closes")
	}
	if event.Type != "server_shutdown" {
		t.Errorf("event.Type = %q, want server_shutdown", event.Type)
	}
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after the shutdown event")
	}
	if n := eb.ClientCount(); n != 0 {
		t.Errorf("ClientCount = %d, want 0 after Shutdown", n)
	}
}

// TestEventBusShutdownDoesNotDoubleCloseWithSubscriberUnsub is the
// regression test for a real production panic: Shutdown() used to close a
// subscriber's channel unconditionally, and so did that subscriber's own
// deferred unsubscribe function (the shape every SSE handler uses) -- e.g.
// server shutdown racing an in-flight request's defer. Whichever ran second
// panicked with "close of closed channel". Runs the race under -race.
func TestEventBusShutdownDoesNotDoubleCloseWithSubscriberUnsub(t *testing.T) {
	eb := NewEventBus()
	_, unsub, err := eb.SubscribeIfUnder(10, 4)
	if err != nil {
		t.Fatalf("SubscribeIfUnder: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); eb.Shutdown() }()
	go func() { defer wg.Done(); unsub() }()
	wg.Wait() // must not panic regardless of which goroutine wins the race

	if n := eb.ClientCount(); n != 0 {
		t.Errorf("ClientCount = %d, want 0 after both Shutdown and unsub ran", n)
	}
}

// TestEventBusSubscribeUnsubClosesChannel covers the ordinary (non-racing)
// path: calling unsub actually closes the channel exactly once, and a
// second unsub call is a safe no-op rather than a double-close panic.
func TestEventBusSubscribeUnsubClosesChannel(t *testing.T) {
	eb := NewEventBus()
	ch, unsub, err := eb.SubscribeIfUnder(10, 4)
	if err != nil {
		t.Fatalf("SubscribeIfUnder: %v", err)
	}
	unsub()
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after unsub")
	}
	// Calling unsub again must not panic.
	unsub()
}
