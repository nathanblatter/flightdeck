package integration

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"flightdeck/internal/mcp"
)

// list_projects is compact by default (no instructions, first-sentence
// summary) and keeps the full shape behind verbosity=full. 20 projects with
// 2 KB of instructions each must come back well under 8 KB in compact form.
func TestListProjectsCompactByDefault(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		p := mkProject(t, st, "proj-"+string(rune('a'+i)))
		if _, err := st.Pool.Exec(ctx, `UPDATE projects SET summary = $1, instructions = $2 WHERE id = $3`,
			"First sentence about the project. "+strings.Repeat("More detail. ", 60),
			strings.Repeat("do the right thing. ", 100), p.ID); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(mcp.NewHandler(st, svc, "test", nil))
	defer srv.Close()

	text := func(res *mcpsdk.CallToolResult) string { return res.Content[0].(*mcpsdk.TextContent).Text }

	compact := text(callTool(t, srv.URL, "list_projects", nil))
	if strings.Contains(compact, "instructions") || strings.Contains(compact, "More detail") {
		t.Fatalf("compact list_projects leaked instructions or full summary:\n%s", compact[:200])
	}
	if len(compact) > 8*1024 {
		t.Fatalf("compact list_projects is %d bytes, want < 8 KB", len(compact))
	}
	var out struct {
		Projects []struct {
			Slug, Summary string
		}
	}
	if err := json.Unmarshal([]byte(compact), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Projects) != 20 || out.Projects[0].Summary != "First sentence about the project." {
		t.Fatalf("compact projects = %d, first summary %q", len(out.Projects), out.Projects[0].Summary)
	}

	full := text(callTool(t, srv.URL, "list_projects", map[string]any{"verbosity": "full"}))
	if !strings.Contains(full, "do the right thing") || !strings.Contains(full, "More detail") || len(full) < 40*1024 {
		t.Fatalf("full list_projects lost fields (%d bytes)", len(full))
	}
}
