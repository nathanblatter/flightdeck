package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"flightdeck/internal/mcp"
	"flightdeck/internal/store"
)

// The compact get_project_context (the MCP default) drops per-item
// boilerplate (uuid, project_id, position, source, created_at, version) and
// caps open items at 25 and recent decisions at 10; verbosity=full keeps the
// full shape and limits. set_project_instructions nudges past 3000 chars.
func TestProjectContextCompactShape(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "slim")
	for i := 0; i < 40; i++ {
		mkItem(t, svc, store.CreateItemParams{ProjectID: p.ID, Title: fmt.Sprintf("open item %02d", i)}, "t")
	}
	for i := 0; i < 20; i++ {
		if _, err := svc.LogActivity(ctx, store.CreateActivityParams{
			ProjectID: p.ID, Kind: ptr("decision"), Actor: ptr("t"), Body: ptr(fmt.Sprintf("decision %02d", i)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(mcp.NewHandler(st, svc, "test", nil))
	defer srv.Close()
	text := func(res *mcpsdk.CallToolResult) string { return res.Content[0].(*mcpsdk.TextContent).Text }

	var compact, full struct {
		OpenItems      []map[string]any `json:"open_items"`
		RecentActivity []map[string]any `json:"recent_activity"`
		Counts         map[string]int   `json:"counts"`
	}
	ctext := text(callTool(t, srv.URL, "get_project_context", map[string]any{"slug": "slim"}))
	if err := json.Unmarshal([]byte(ctext), &compact); err != nil {
		t.Fatal(err)
	}
	if len(compact.OpenItems) != 25 || len(compact.RecentActivity) != 10 || compact.Counts["backlog"] != 40 {
		t.Fatalf("compact: open=%d recent=%d counts=%v", len(compact.OpenItems), len(compact.RecentActivity), compact.Counts)
	}
	it := compact.OpenItems[0]
	for _, k := range []string{"id", "project_id", "position", "source", "created_at", "version"} {
		if _, ok := it[k]; ok {
			t.Fatalf("compact open item still carries %q: %v", k, it)
		}
	}
	for _, k := range []string{"ref", "title", "status", "priority", "type", "updated_at"} {
		if _, ok := it[k]; !ok {
			t.Fatalf("compact open item lost %q: %v", k, it)
		}
	}

	ftext := text(callTool(t, srv.URL, "get_project_context", map[string]any{"slug": "slim", "verbosity": "full"}))
	if err := json.Unmarshal([]byte(ftext), &full); err != nil {
		t.Fatal(err)
	}
	if len(full.OpenItems) != 40 || len(full.RecentActivity) != 15 {
		t.Fatalf("full: open=%d recent=%d", len(full.OpenItems), len(full.RecentActivity))
	}
	if _, ok := full.OpenItems[0]["id"]; !ok {
		t.Fatalf("full open item lost id: %v", full.OpenItems[0])
	}
	if len(ctext) >= len(ftext)/2 {
		t.Fatalf("compact bundle %d bytes is not materially smaller than full %d", len(ctext), len(ftext))
	}

	// Instructions nudge: short is silent, long is flagged.
	short := text(callTool(t, srv.URL, "set_project_instructions", map[string]any{"slug": "slim", "instructions": "go test ./..."}))
	if strings.Contains(short, "nudges") {
		t.Fatalf("short instructions nudged: %s", short)
	}
	long := text(callTool(t, srv.URL, "set_project_instructions", map[string]any{"slug": "slim", "instructions": strings.Repeat("x", 3001)}))
	if !strings.Contains(long, `"nudges"`) || !strings.Contains(long, "3001 chars") {
		t.Fatalf("long instructions not nudged: %s", long[len(long)-200:])
	}
}
