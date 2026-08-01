package service

import (
	"testing"
	"time"
)

func TestHubBroadcastAndUnsubscribe(t *testing.T) {
	h := NewHub()
	a := h.Subscribe()
	b := h.Subscribe()
	if h.Subscribers() != 2 {
		t.Fatalf("subscribers = %d, want 2", h.Subscribers())
	}

	h.Broadcast([]byte("hello"))
	for _, ch := range []chan []byte{a, b} {
		select {
		case msg := <-ch:
			if string(msg) != "hello" {
				t.Fatalf("got %q, want hello", msg)
			}
		case <-time.After(time.Second):
			t.Fatal("subscriber did not receive broadcast")
		}
	}

	h.Unsubscribe(a)
	if h.Subscribers() != 1 {
		t.Fatalf("after unsubscribe, subscribers = %d, want 1", h.Subscribers())
	}
	if _, open := <-a; open {
		t.Fatal("unsubscribed channel should be closed")
	}
	// Double unsubscribe must be a no-op, not a panic/second close.
	h.Unsubscribe(a)
}

// TestHubSlowClientNeverBlocks fills a subscriber's buffer and asserts Broadcast
// still returns promptly (dropping for the slow client) rather than blocking the
// publisher — the property that lets a broadcast run on the write path.
func TestHubSlowClientNeverBlocks(t *testing.T) {
	h := NewHub()
	slow := h.Subscribe() // never drained
	defer h.Unsubscribe(slow)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10_000; i++ {
			h.Broadcast([]byte("x"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast blocked on a slow client")
	}
}
