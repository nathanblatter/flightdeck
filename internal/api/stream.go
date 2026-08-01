package api

import (
	"fmt"
	"net/http"
)

// stream is a Server-Sent Events endpoint that pushes live mutation events to
// the web UI, replacing per-interval polling. It subscribes to the service's
// in-process Hub (fed by every write via enqueue) and relays each JSON payload
// as an SSE `data:` frame until the client disconnects.
//
// Auth note: browser EventSource can't set an X-API-Key header, so this route is
// reached through the same auth middleware but relies on its query-param
// fallback (?api_key=). The request logger records only the path, not the query.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Defeat proxy buffering (nginx/Cloudflare) so events aren't held back.
	w.Header().Set("X-Accel-Buffering", "no")

	hub := s.Svc.Hub()
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	// Prelude comment opens the stream immediately and tells EventSource how long
	// to wait before reconnecting after a drop.
	fmt.Fprint(w, ": connected\nretry: 3000\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case payload, open := <-ch:
			if !open {
				return
			}
			// One SSE frame. Payload is single-line JSON from enqueue, so no
			// multi-line escaping is needed.
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
