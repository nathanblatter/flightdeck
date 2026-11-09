package integration

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"flightdeck/internal/mcp"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

// A project is work/normal unless told otherwise, and both tags round-trip
// through create and update.
func TestProjectDomainDefaultsAndUpdate(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "plain")
	if p.Domain != "work" || p.Sensitivity != "normal" {
		t.Fatalf("defaults = %s/%s, want work/normal", p.Domain, p.Sensitivity)
	}
	life, private := "life", "private"
	upd, err := svc.UpdateProject(ctx, store.UpdateProjectParams{Slug: "plain", Domain: &life, Sensitivity: &private})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if upd.Domain != "life" || upd.Sensitivity != "private" {
		t.Fatalf("after update = %s/%s", upd.Domain, upd.Sensitivity)
	}
	bad := "secret"
	if _, err := st.UpdateProject(ctx, store.UpdateProjectParams{Slug: "plain", Sensitivity: &bad}); err == nil {
		t.Fatal("CHECK constraint let an invalid sensitivity through")
	}
	if err := service.ValidateProjectTags(nil, strp("home"), nil); err == nil {
		t.Fatal("ValidateProjectTags accepted domain=home")
	}
}

// Private projects are absent from every cross-project surface and present
// whenever the call names the project.
func TestPrivateProjectHiddenUnlessNamed(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	pub := mkProject(t, st, "public")
	priv, err := st.CreateProject(ctx, store.CreateProjectParams{Slug: "secret", Name: "secret", Sensitivity: strp("private")})
	if err != nil {
		t.Fatalf("create private: %v", err)
	}
	mkItem(t, svc, store.CreateItemParams{ProjectID: pub.ID, Title: "public zebra migration", Status: strp("in_progress")}, "t")
	privItem := mkItem(t, svc, store.CreateItemParams{ProjectID: priv.ID, Title: "secret zebra migration", Status: strp("in_progress"), Type: strp("bug")}, "t")
	if _, err := svc.LogActivity(ctx, store.CreateActivityParams{ProjectID: priv.ID, Kind: strp("decision"), Body: strp("zebra decision in the secret project")}); err != nil {
		t.Fatalf("log: %v", err)
	}
	if _, err := st.Pool.Exec(ctx, `UPDATE items SET updated_at = now() - interval '30 days', status = 'backlog', type = 'bug', created_at = now() - interval '30 days'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO tool_calls (tool, actor, project, ok, duration_ms, result_bytes) VALUES ('get_item','t','secret',true,1,1), ('get_item','t','public',true,1,1)`); err != nil {
		t.Fatalf("seed tool_calls: %v", err)
	}
	privID := pgtype.UUID{Bytes: priv.ID, Valid: true}

	// get_global_context
	global, err := svc.GlobalContext(ctx, service.VerbosityCompact, "")
	if err != nil {
		t.Fatalf("global: %v", err)
	}
	for _, po := range global.Projects {
		if po.Project.Slug == "secret" {
			t.Fatal("private project present in global context")
		}
	}
	if len(global.Projects) != 1 || global.Projects[0].Project.Slug != "public" {
		t.Fatalf("global projects = %+v", global.Projects)
	}

	// search without a project filter: items and activity both hidden
	items, acts, err := svc.SearchSmart(ctx, "zebra", pgtype.UUID{}, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, it := range items {
		if it.ID == privItem.ID.String() {
			t.Fatal("private item leaked from unfiltered search")
		}
	}
	for _, a := range acts {
		if a.ProjectID == priv.ID.String() {
			t.Fatal("private activity leaked from unfiltered search")
		}
	}
	if len(items) != 1 {
		t.Fatalf("unfiltered search: %d items (want 1)", len(items))
	}
	// trigram fallback path too
	items, _, err = svc.SearchSmart(ctx, "zebr migrat", pgtype.UUID{}, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("search fuzzy: %v", err)
	}
	for _, it := range items {
		if it.ID == privItem.ID.String() {
			t.Fatal("private item leaked from fuzzy search")
		}
	}
	// ...present when the project is named
	items, acts, err = svc.SearchSmart(ctx, "zebra", privID, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("search named: %v", err)
	}
	if len(items) != 1 || items[0].ID != privItem.ID.String() || len(acts) == 0 {
		t.Fatalf("named search: %d items, %d activity (want 1, >=1)", len(items), len(acts))
	}

	// get_project_context by slug still works
	pc, err := svc.ProjectContext(ctx, "secret", service.VerbosityCompact)
	if err != nil {
		t.Fatalf("project context: %v", err)
	}
	if pc.Project.Sensitivity != "private" || len(pc.OpenItems) != 1 {
		t.Fatalf("project context = %+v", pc.Project)
	}

	// list_items with the project filter
	page, err := st.ListItems(ctx, store.ListItemsParams{ProjectID: privID})
	if err != nil || len(page) != 1 {
		t.Fatalf("list_items(project=secret) = %d, %v", len(page), err)
	}

	// next_action / stale
	next, err := svc.NextAction(ctx, "", 10)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	for _, it := range next.Items {
		if it.ProjectSlug == "secret" {
			t.Fatal("private item in next_action")
		}
	}
	next, err = svc.NextAction(ctx, "secret", 10)
	if err != nil || len(next.Items) != 1 {
		t.Fatalf("next_action(secret) = %d, %v", len(next.Items), err)
	}
	stale, err := svc.Stale(ctx, time.Now(), time.Now())
	if err != nil {
		t.Fatalf("stale: %v", err)
	}
	for _, it := range stale.UntriagedBugs {
		if it.ProjectSlug == "secret" {
			t.Fatal("private bug in stale report")
		}
	}
	if len(stale.UntriagedBugs) != 1 {
		t.Fatalf("stale bugs = %d, want 1", len(stale.UntriagedBugs))
	}

	// usage_report top_projects
	rep, err := svc.UsageReport(ctx, 7, nil)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	for _, p := range rep.TopProjects {
		if p.Project == "secret" {
			t.Fatal("private project in usage top_projects")
		}
	}
	seenPublic := false
	for _, p := range rep.TopProjects {
		seenPublic = seenPublic || p.Project == "public"
	}
	if !seenPublic {
		t.Fatalf("top_projects lost the normal project: %+v", rep.TopProjects)
	}

	// MCP list_projects hides it; the brief omits default tags.
	srv := httptest.NewServer(mcp.NewHandler(st, svc, "test", nil))
	defer srv.Close()
	text := func(res *mcpsdk.CallToolResult) string { return res.Content[0].(*mcpsdk.TextContent).Text }
	listed := text(callTool(t, srv.URL, "list_projects", nil))
	if strings.Contains(listed, `"secret"`) {
		t.Fatalf("list_projects leaked the private project:\n%s", listed)
	}
	if strings.Contains(listed, `"domain"`) || strings.Contains(listed, `"sensitivity"`) {
		t.Fatalf("list_projects spends tokens on default tags:\n%s", listed)
	}
	// update_project flips it back to normal and it reappears.
	callTool(t, srv.URL, "update_project", map[string]any{"slug": "secret", "sensitivity": "normal", "domain": "life"})
	var out struct {
		Projects []struct{ Slug, Domain, Sensitivity string }
	}
	if err := json.Unmarshal([]byte(text(callTool(t, srv.URL, "list_projects", nil))), &out); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range out.Projects {
		if p.Slug == "secret" {
			found = p.Domain == "life" && p.Sensitivity == ""
		}
	}
	if !found {
		t.Fatalf("after update_project: %+v", out.Projects)
	}
}

// The domain filter on get_global_context and list_projects returns only
// matching projects; absent means every domain.
func TestGlobalContextDomainFilter(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	for slug, dom := range map[string]string{"job": "work", "class": "school", "car": "life", "repo": "work"} {
		if _, err := st.CreateProject(ctx, store.CreateProjectParams{Slug: slug, Name: slug, Domain: strp(dom)}); err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
	}
	slugs := func(domain string) []string {
		g, err := svc.GlobalContext(ctx, service.VerbosityCompact, domain)
		if err != nil {
			t.Fatalf("global %q: %v", domain, err)
		}
		out := make([]string, 0, len(g.Projects))
		for _, p := range g.Projects {
			out = append(out, p.Project.Slug)
		}
		return out
	}
	if got := slugs(""); len(got) != 4 {
		t.Fatalf("all domains = %v", got)
	}
	if got := slugs("work"); len(got) != 2 || (got[0] != "job" && got[0] != "repo") {
		t.Fatalf("work = %v", got)
	}
	if got := slugs("life"); len(got) != 1 || got[0] != "car" {
		t.Fatalf("life = %v", got)
	}
	if _, err := st.ListProjects(ctx, store.ListProjectsParams{Domain: strp("school")}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mcp.NewHandler(st, svc, "test", nil))
	defer srv.Close()
	text := func(res *mcpsdk.CallToolResult) string { return res.Content[0].(*mcpsdk.TextContent).Text }
	school := text(callTool(t, srv.URL, "list_projects", map[string]any{"domain": "school"}))
	if !strings.Contains(school, `"class"`) || strings.Contains(school, `"job"`) {
		t.Fatalf("list_projects(domain=school) = %s", school)
	}
}
