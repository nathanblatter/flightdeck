package service

import "sync"

// Hub is an in-process publish/subscribe bus for live UI updates. Every mutation
// already writes a JSON event to the durable webhook outbox (see enqueue); Hub
// additionally fans that same payload out to any connected SSE clients so the
// kanban updates live instead of polling. It is deliberately EPHEMERAL and
// best-effort — unlike the outbox it makes no delivery guarantee: a client that
// isn't connected (or can't keep up) simply misses events and reconciles with a
// full refetch on its next (re)connect. That is what lets a broadcast never
// block a database write.
//
// Concurrency: one bounded channel per subscriber. Broadcast holds the lock only
// to iterate subscribers and does a non-blocking send, so a slow or dead client
// can never stall a publisher or another subscriber.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func NewHub() *Hub { return &Hub{subs: make(map[chan []byte]struct{})} }

// Subscribe registers a new client and returns its event channel. The buffer
// absorbs short bursts; when it's full Broadcast drops for that client (see
// below). Always pair with Unsubscribe.
func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes and closes a client channel. Safe to call once per
// Subscribe; a double call is a no-op.
func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// Broadcast delivers payload to every subscriber without blocking. A subscriber
// whose buffer is full is skipped (it's slow/stuck) — it will catch up via a
// refetch, which is cheaper than letting one bad client back-pressure writes.
func (h *Hub) Broadcast(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- payload:
		default:
		}
	}
}

// Subscribers reports the current connected-client count (for /metrics or tests).
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
