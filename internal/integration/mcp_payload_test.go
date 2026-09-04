package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"flightdeck/internal/auth"
	"flightdeck/internal/mcp"
	"flightdeck/internal/store"
)

func ptr[T any](v T) *T { return &v }

// callTool runs one tools/call against a real MCP server over streamable HTTP.
func callTool(t *testing.T, url, tool string, args map[string]any) *mcpsdk.CallToolResult {
	return callToolWithClient(t, url, tool, args, nil)
}

func callToolWithClient(t *testing.T, url, tool string, args map[string]any, httpClient *http.Client) *mcpsdk.CallToolResult {
	t.Helper()
	ctx := context.Background()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: url, HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sess.Close()
	res, err := sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", tool, err)
	}
	if res.IsError {
		t.Fatalf("call %s returned tool error: %+v", tool, res.Content)
	}
	return res
}

type apiKeyTransport struct {
	key string
}

func (t apiKeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("X-API-Key", t.key)
	return http.DefaultTransport.RoundTrip(req)
}

// Tool results must carry their JSON exactly once — a single text block, no
// structuredContent copy. The SDK's typed-output path emitted both, doubling
// the token cost of every call (usage_report showed get_global_context at
// 85 KB avg); addTool's wrapper exists to prevent that from coming back.
func TestMCPResultsSingleEncoded(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "gamma")
	if _, err := st.Pool.Exec(ctx, `UPDATE projects SET instructions = $1 WHERE id = $2`,
		strings.Repeat("build with care. ", 200), p.ID); err != nil {
		t.Fatalf("set instructions: %v", err)
	}
	if _, err := svc.CreateItem(ctx, store.CreateItemParams{
		ProjectID: p.ID, Title: "big item", Body: ptr(strings.Repeat("lorem ipsum ", 300)),
	}, "tester"); err != nil {
		t.Fatalf("create item: %v", err)
	}

	srv := httptest.NewServer(mcp.NewHandler(st, svc, "test", nil))
	defer srv.Close()

	res := callTool(t, srv.URL, "get_global_context", nil)
	if res.StructuredContent != nil {
		t.Fatalf("structuredContent must be empty (payload would be double-encoded), got %v", res.StructuredContent)
	}
	if len(res.Content) != 1 {
		t.Fatalf("want exactly 1 content block, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}

	// Compact global view: brief top items (no bodies) and no instructions.
	var global struct {
		Projects []struct {
			Project  map[string]any   `json:"project"`
			TopItems []map[string]any `json:"top_items"`
		} `json:"projects"`
	}
	if err := json.Unmarshal([]byte(text.Text), &global); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if len(global.Projects) != 1 || len(global.Projects[0].TopItems) != 1 {
		t.Fatalf("unexpected global shape: %s", text.Text)
	}
	if _, has := global.Projects[0].TopItems[0]["body"]; has {
		t.Errorf("compact top_items should be briefs without bodies")
	}
	if v := global.Projects[0].Project["instructions"]; v != nil && v != "" {
		t.Errorf("compact global should omit instructions, got %v", v)
	}
	if global.Projects[0].TopItems[0]["ref"] != "gamma-1" {
		t.Errorf("brief should keep the ref, got %v", global.Projects[0].TopItems[0])
	}

	// Full verbosity keeps instructions for the rare debugging read.
	res = callTool(t, srv.URL, "get_global_context", map[string]any{"verbosity": "full"})
	full := res.Content[0].(*mcpsdk.TextContent).Text
	if !strings.Contains(full, "build with care.") {
		t.Errorf("full global should include instructions")
	}
}

func TestMCPRecordContextImpact(t *testing.T) {
	st, svc := setup(t)
	mkProject(t, st, "alpha")
	if _, err := st.Pool.Exec(context.Background(), `TRUNCATE api_keys`); err != nil {
		t.Fatal(err)
	}
	readKey := "fd_test_mcp_impact_read"
	writeKey := "fd_test_mcp_impact_write"
	for _, key := range []store.CreateAPIKeyParams{
		{Name: "read-agent", KeyHash: auth.HashKey(readKey), Scopes: []string{auth.ScopeRead}},
		{Name: "write-agent", KeyHash: auth.HashKey(writeKey), Scopes: []string{auth.ScopeWrite}},
	} {
		if _, err := st.CreateAPIKey(context.Background(), key); err != nil {
			t.Fatal(err)
		}
	}
	handler := auth.Middleware(st, auth.ScopeWrite)(mcp.NewHandler(st, svc, "test", nil))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", readKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-only MCP status = %d, want 403", resp.StatusCode)
	}

	res := callToolWithClient(t, srv.URL, "record_context_impact", map[string]any{
		"session_id": "mcp-session", "project": "alpha",
		"effect": "harmful", "mechanism": "stale_or_incorrect",
		"context_refs":            []string{"alpha summary"},
		"evidence":                "the summary contained an outdated claim",
		"estimated_minutes_delta": -5,
	}, &http.Client{Transport: apiKeyTransport{key: writeKey}})
	if len(res.Content) != 1 {
		t.Fatalf("want one result block, got %d", len(res.Content))
	}
	var event struct {
		ID, Actor, Project, Effect, Mechanism, Evidence string
	}
	text, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", res.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), &event); err != nil {
		t.Fatal(err)
	}
	if event.ID == "" || event.Actor != "write-agent" || event.Project != "alpha" ||
		event.Effect != "harmful" || event.Mechanism != "stale_or_incorrect" ||
		event.Evidence != "the summary contained an outdated claim" {
		t.Fatalf("event = %+v", event)
	}
	if n := countRows(t, st, `SELECT count(*) FROM context_impact_events`); n != 1 {
		t.Fatalf("impact rows = %d, want 1", n)
	}
}
