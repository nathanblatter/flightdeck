package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"flightdeck/internal/mcp"
	"flightdeck/internal/update"
)

// A published release newer than the running build must surface as an upgrade
// prompt on both orient calls — that's how the notice reaches whichever agent
// session touches the instance first.
func TestUpdateNoticeOnOrient(t *testing.T) {
	st, svc := setup(t)
	mkProject(t, st, "delta")

	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.2.0","html_url":"https://example.com/rel"}`))
	}))
	defer github.Close()
	t.Setenv("FLIGHTDECK_UPDATE_API", github.URL)
	upd := update.New("v0.1.0")
	if err := upd.CheckNow(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}

	srv := httptest.NewServer(mcp.NewHandler(st, svc, "v0.1.0", upd))
	defer srv.Close()

	res := callTool(t, srv.URL, "get_project_context", map[string]any{"slug": "delta"})
	var pc struct {
		Nudges []string `json:"nudges"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &pc); err != nil {
		t.Fatalf("unmarshal project context: %v", err)
	}
	if !hasUpdateLine(pc.Nudges) {
		t.Errorf("project context nudges missing update notice: %v", pc.Nudges)
	}

	res = callTool(t, srv.URL, "get_global_context", nil)
	var gc struct {
		Notices []string `json:"notices"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &gc); err != nil {
		t.Fatalf("unmarshal global context: %v", err)
	}
	if !hasUpdateLine(gc.Notices) {
		t.Errorf("global context notices missing update notice: %v", gc.Notices)
	}
}

func hasUpdateLine(lines []string) bool {
	for _, l := range lines {
		if strings.Contains(l, "v0.2.0") && strings.Contains(l, "flightdeck update") {
			return true
		}
	}
	return false
}
