package integration

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"flightdeck/internal/api"
	"flightdeck/internal/auth"
	"flightdeck/internal/store"
)

// TestStreamSSEEndToEnd drives the live-update path through real HTTP: it
// authenticates the SSE stream via the query-param fallback (EventSource can't
// set headers), subscribes, then performs a mutation and asserts the broadcast
// arrives as an SSE data frame.
func TestStreamSSEEndToEnd(t *testing.T) {
	st, svc := setup(t)
	p := mkProject(t, st, "sse")
	ctx := context.Background()

	// api_keys isn't in setup's truncate set, so use a per-run unique secret to
	// avoid colliding with a prior run's key hash.
	raw := fmt.Sprintf("fd_ssetest_%d", time.Now().UnixNano())
	if _, err := st.CreateAPIKey(ctx, store.CreateAPIKeyParams{
		Name: "sse", KeyHash: auth.HashKey(raw), Scopes: []string{"read", "write"},
	}); err != nil {
		t.Fatalf("create key: %v", err)
	}

	srv := httptest.NewServer(api.New(st, svc).Routes())
	defer srv.Close()

	streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, srv.URL+"/api/stream?api_key="+raw, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}

	br := bufio.NewReader(resp.Body)
	// Reading the prelude confirms the server-side subscription is registered
	// (Subscribe runs before the prelude is written), so a mutation now can't be
	// missed.
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatalf("read prelude: %v", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "live item"}, "sse")
	}()

	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream (no data frame before timeout): %v", err)
		}
		if strings.HasPrefix(line, "data:") {
			if !strings.Contains(line, "item.created") {
				t.Fatalf("unexpected SSE frame: %s", line)
			}
			return // got the live event
		}
	}
}
