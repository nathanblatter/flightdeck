package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"flightdeck/internal/auth"
	"flightdeck/internal/mcp"
	"flightdeck/internal/store"
)

// record_context_impact without session_id stores the MCP session id; the
// impact nudge rides on exactly the first successful write of a session and
// never on reads; a second session gets its own nudge. A session that writes
// before orienting also gets the orient nudge, naming the project when the
// write carried one; an oriented session never sees it.
func TestImpactDefaultsToMCPSessionAndNudgesOnce(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, `TRUNCATE tool_calls, context_impact_events`); err != nil {
		t.Fatal(err)
	}
	p := mkProject(t, st, "imp")

	// The MCP handler needs an authenticated actor for impact events.
	h := mcp.NewHandler(st, svc, "test", nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{Name: "tester"})))
	}))
	defer srv.Close()
	item := mkItem(t, svc, store.CreateItemParams{ProjectID: p.ID, Title: "nudge target"}, "tester")

	connect := func() *mcpsdk.ClientSession {
		t.Helper()
		client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
		sess, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: srv.URL}, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		return sess
	}
	call := func(sess *mcpsdk.ClientSession, tool string, args map[string]any) *mcpsdk.CallToolResult {
		t.Helper()
		res, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: tool, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("call %s: err=%v res=%+v", tool, err, res)
		}
		return res
	}
	hasText := func(res *mcpsdk.CallToolResult, needle string) bool {
		for _, c := range res.Content[1:] {
			if tc, ok := c.(*mcpsdk.TextContent); ok && strings.Contains(tc.Text, needle) {
				return true
			}
		}
		return false
	}
	hasNudge := func(res *mcpsdk.CallToolResult) bool { return hasText(res, "record_context_impact") }
	hasOrientNudge := func(res *mcpsdk.CallToolResult) bool { return hasText(res, "wrote before reading project context") }

	s1 := connect()
	defer s1.Close()
	if hasNudge(call(s1, "get_project_context", map[string]any{"slug": "imp"})) {
		t.Fatal("read carried the nudge")
	}
	first := call(s1, "log_activity", map[string]any{"project": "imp", "kind": "progress", "body": "one"})
	if len(first.Content) != 2 || !hasNudge(first) || hasOrientNudge(first) {
		t.Fatalf("oriented session's first write should carry exactly the impact nudge, got %d blocks", len(first.Content))
	}
	second := call(s1, "log_activity", map[string]any{"project": "imp", "kind": "progress", "body": "two"})
	if len(second.Content) != 1 || hasNudge(second) {
		t.Fatalf("second write should not be nudged, got %d blocks", len(second.Content))
	}

	// No session_id: stored as the MCP session id, the same one tool_calls has.
	call(s1, "record_context_impact", map[string]any{
		"project": "imp", "effect": "neutral", "mechanism": "ignored", "evidence": "nothing applied",
	})
	var mcpSession, impactSession string
	if err := st.Pool.QueryRow(ctx, `SELECT DISTINCT session_id FROM tool_calls WHERE session_id <> ''`).Scan(&mcpSession); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool.QueryRow(ctx, `SELECT session_id FROM context_impact_events`).Scan(&impactSession); err != nil {
		t.Fatal(err)
	}
	if mcpSession == "" || impactSession != mcpSession {
		t.Fatalf("impact session_id = %q, want MCP session %q", impactSession, mcpSession)
	}
	// An explicit session_id still wins.
	call(s1, "record_context_impact", map[string]any{
		"session_id": "explicit", "project": "imp", "effect": "neutral", "mechanism": "ignored", "evidence": "x",
	})
	var n int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM context_impact_events WHERE session_id = 'explicit'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("explicit session_id not honoured (%d rows)", n)
	}

	// A fresh session that writes without orienting gets both nudges; the
	// orient hint falls back to get_global_context when the write names no
	// project.
	s2 := connect()
	defer s2.Close()
	blind := call(s2, "update_item", map[string]any{"id": item.Ref, "status": "todo"})
	if len(blind.Content) != 3 || !hasNudge(blind) || !hasOrientNudge(blind) || !hasText(blind, "get_global_context") {
		t.Fatalf("unoriented session's first write should carry impact + orient nudges, got %d blocks: %+v", len(blind.Content), blind.Content)
	}
	if n := len(call(s2, "log_activity", map[string]any{"project": "imp", "kind": "progress", "body": "x"}).Content); n != 1 {
		t.Fatalf("unoriented session's second write should not be nudged again, got %d blocks", n)
	}

	// A write that names a project gets a get_project_context(slug) hint.
	s3 := connect()
	defer s3.Close()
	named := call(s3, "log_activity", map[string]any{"project": "imp", "kind": "progress", "body": "y"})
	if !hasOrientNudge(named) || !hasText(named, `get_project_context("imp")`) {
		t.Fatalf("orient nudge should name the project, got %+v", named.Content)
	}
}
