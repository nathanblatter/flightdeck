package integration

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"flightdeck/internal/dto"
	"flightdeck/internal/mcp"
	"flightdeck/internal/service"
)

// Calls on one MCP session share a non-empty session_id in tool_calls (and
// search_log); a REST-style call with no session records ”. The initialize
// response carries the agent-contract instructions.
func TestSessionTelemetry(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, `TRUNCATE tool_calls, search_log`); err != nil {
		t.Fatal(err)
	}
	mkProject(t, st, "sess")

	srv := httptest.NewServer(mcp.NewHandler(st, svc, "test", nil))
	defer srv.Close()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: srv.URL}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()

	ir := sess.InitializeResult()
	if ir == nil || ir.Instructions != mcp.Instructions || !strings.Contains(ir.Instructions, "get_project_context") {
		t.Fatalf("initialize instructions = %q", ir.Instructions)
	}
	if len(mcp.Instructions) > 600 {
		t.Fatalf("instructions are %d chars, want <= 600", len(mcp.Instructions))
	}

	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"get_project_context", map[string]any{"slug": "sess"}},
		{"search", map[string]any{"query": "anything"}},
		{"log_activity", map[string]any{"project": "sess", "kind": "progress", "body": "hi"}},
	} {
		res, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: call.tool, Arguments: call.args})
		if err != nil || res.IsError {
			t.Fatalf("call %s: err=%v res=%+v", call.tool, err, res)
		}
	}
	// A caller with no session (REST path) records ''.
	svc.RecordToolCall(ctx, service.ToolCall{Tool: "get_item", Actor: "rest", OK: true})

	var ids []string
	rows, err := st.Pool.Query(ctx, `SELECT DISTINCT session_id FROM tool_calls WHERE tool <> 'get_item'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("MCP calls should share one non-empty session_id, got %q", ids)
	}
	var n int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM tool_calls WHERE session_id = $1`, ids[0]).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("calls on session = %d, want 3", n)
	}
	var restSession string
	if err := st.Pool.QueryRow(ctx, `SELECT session_id FROM tool_calls WHERE tool = 'get_item'`).Scan(&restSession); err != nil {
		t.Fatal(err)
	}
	if restSession != "" {
		t.Fatalf("REST call session_id = %q, want ''", restSession)
	}
	var searchSession string
	if err := st.Pool.QueryRow(ctx, `SELECT session_id FROM search_log`).Scan(&searchSession); err != nil {
		t.Fatal(err)
	}
	if searchSession != ids[0] {
		t.Fatalf("search_log session_id = %q, want %q", searchSession, ids[0])
	}

	// The MCP usage_report carries the sessions block for this one session,
	// which oriented before writing.
	res, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: "usage_report", Arguments: map[string]any{"days": 1}})
	if err != nil || res.IsError {
		t.Fatalf("usage_report: err=%v res=%+v", err, res)
	}
	var rep dto.UsageReport
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Sessions.Sessions != 1 || rep.Sessions.Oriented != 1 || rep.Sessions.WroteWithoutOrient != 0 || rep.Sessions.OrientRate != 1 {
		t.Fatalf("sessions block = %+v", rep.Sessions)
	}
}

// Session classification: orient-first, write-first, orient-after-write,
// read-only and impact-reporting sessions each land in the right bucket;
// REST rows (session ”) are excluded.
func TestSessionClassification(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, `TRUNCATE tool_calls`); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	seq := 0
	rec := func(session, tool string) {
		t.Helper()
		seq++
		svc.RecordToolCall(ctx, service.ToolCall{Tool: tool, Actor: "a", OK: true, SessionID: session})
		// Force a strictly increasing timeline regardless of insert speed.
		if _, err := st.Pool.Exec(ctx, `UPDATE tool_calls SET called_at = $1 WHERE id = (SELECT max(id) FROM tool_calls)`,
			base.Add(time.Duration(seq)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	// orient-first: reads, then writes.
	rec("s-orient", "get_project_context")
	rec("s-orient", "update_item")
	rec("s-orient", "log_activity")
	// write-first: writes, then orients too late.
	rec("s-write", "log_activity")
	rec("s-write", "get_project_context")
	// never orients, only writes.
	rec("s-blind", "create_item")
	// global orient counts; list_projects alone does not.
	rec("s-global", "get_global_context")
	rec("s-global", "update_item")
	rec("s-list", "list_projects")
	rec("s-list", "update_item")
	// read-only session is oriented; impact-reporting session is counted.
	rec("s-read", "get_project_context")
	rec("s-impact", "get_project_context")
	rec("s-impact", "update_item")
	rec("s-impact", "record_context_impact")
	// REST rows carry no session and must not be a session of their own.
	rec("", "update_item")
	rec("", "get_project_context")

	rep, err := svc.UsageReport(ctx, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := rep.Sessions
	want := dto.SessionUsage{Sessions: 7, Oriented: 4, WroteWithoutOrient: 3, ImpactReported: 1, OrientRate: 4.0 / 7, AvgCalls: 14.0 / 7}
	if got != want {
		t.Fatalf("sessions = %+v, want %+v", got, want)
	}
}
