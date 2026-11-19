package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"flightdeck/internal/api"
	"flightdeck/internal/auth"
	"flightdeck/internal/blob"
	"flightdeck/internal/dto"
	"flightdeck/internal/mcp"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

// mkKey stores a key with a per-run unique secret and returns the raw secret.
// projects nil = unrestricted.
func mkKey(t *testing.T, st *store.Store, name string, scopes []string, projects []uuid.UUID) string {
	t.Helper()
	raw := fmt.Sprintf("fd_projkeys_%s_%d", name, time.Now().UnixNano())
	if _, err := st.CreateAPIKey(context.Background(), store.CreateAPIKeyParams{
		Name: name, KeyHash: auth.HashKey(raw), Scopes: scopes, ProjectIds: projects,
	}); err != nil {
		t.Fatalf("create key %s: %v", name, err)
	}
	return raw
}

func errorMsg(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Error
}

func capture(t *testing.T, ts *httptest.Server, key, project string) (*http.Response, []byte) {
	t.Helper()
	return doJSON(t, http.MethodPost, ts.URL+"/api/ingest/capture", key,
		map[string]any{"project": project, "title": "from " + project, "type": "bug"}, nil)
}

// A restricted ingest key files into its project and nowhere else; an
// unrestricted ingest key keeps today's behaviour.
func TestRestrictedKeyIngestWrites(t *testing.T) {
	ts, st, _, openKey := setupHTTP(t)
	alpha := mkProject(t, st, "alpha")
	mkProject(t, st, "beta")
	restricted := mkKey(t, st, "evals", []string{auth.ScopeIngest}, []uuid.UUID{alpha.ID})

	if res, body := capture(t, ts, restricted, "alpha"); res.StatusCode != http.StatusCreated {
		t.Fatalf("capture into own project: %d %s", res.StatusCode, body)
	}
	for _, target := range []string{"beta", "no-such-project"} {
		res, body := capture(t, ts, restricted, target)
		if res.StatusCode != http.StatusForbidden || !strings.Contains(errorMsg(body), "not allowed") {
			t.Fatalf("capture into %s: %d %s, want 403", target, res.StatusCode, body)
		}
	}
	res, body := doJSON(t, http.MethodPost, ts.URL+"/api/ingest/bug", restricted,
		map[string]any{"site": "beta", "message": "cross-project bug"}, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("bug into beta: %d %s, want 403", res.StatusCode, body)
	}
	res, body = doJSON(t, http.MethodPost, ts.URL+"/api/ingest/bug", restricted,
		map[string]any{"site": "alpha", "message": "own bug"}, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("bug into alpha: %d %s", res.StatusCode, body)
	}
	if n := countRows(t, st, `SELECT count(*) FROM items i JOIN projects p ON p.id = i.project_id WHERE p.slug = 'beta'`); n != 0 {
		t.Fatalf("beta has %d items after refused writes", n)
	}

	// Unrestricted ingest key: unchanged (any project; unknown slug is 404).
	if res, body := capture(t, ts, openKey, "beta"); res.StatusCode != http.StatusCreated {
		t.Fatalf("unrestricted capture into beta: %d %s", res.StatusCode, body)
	}
	if res, body := capture(t, ts, openKey, "no-such-project"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("unrestricted capture into unknown: %d %s, want 404", res.StatusCode, body)
	}
}

func TestRestrictedKeyProjectListing(t *testing.T) {
	ts, st, _, openKey := setupHTTP(t)
	alpha := mkProject(t, st, "alpha")
	gamma := mkProject(t, st, "gamma")
	mkProject(t, st, "beta")
	restricted := mkKey(t, st, "evals", []string{auth.ScopeIngest}, []uuid.UUID{alpha.ID, gamma.ID})

	list := func(key string) []string {
		res, body := doJSON(t, http.MethodGet, ts.URL+"/api/ingest/projects", key, nil, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("ingest/projects: %d %s", res.StatusCode, body)
		}
		var slugs []string
		if err := json.Unmarshal(body, &slugs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		slices.Sort(slugs)
		return slugs
	}
	if got := list(restricted); !slices.Equal(got, []string{"alpha", "gamma"}) {
		t.Fatalf("restricted listing = %v, want [alpha gamma]", got)
	}
	if got := list(openKey); !slices.Equal(got, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("unrestricted listing = %v", got)
	}
}

func TestRestrictedKeyAttachmentGuard(t *testing.T) {
	ts, st, svc, openKey := setupHTTP(t)
	svc.SetBlob(blob.NewMemory())
	alpha := mkProject(t, st, "alpha")
	mkProject(t, st, "beta")
	restricted := mkKey(t, st, "evals", []string{auth.ScopeIngest}, []uuid.UUID{alpha.ID})

	// A fresh capture item in beta (filed by the unrestricted key) passes the
	// source/freshness guard, so only the project restriction can refuse it.
	res, body := capture(t, ts, openKey, "beta")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("seed beta: %d %s", res.StatusCode, body)
	}
	var betaItem dto.Item
	_ = json.Unmarshal(body, &betaItem)
	res, body = multipartUpload(t, ts.URL+"/api/ingest/attachments/"+betaItem.ID, restricted, map[string][]byte{"a.png": pngBytes})
	if res.StatusCode != http.StatusForbidden || !strings.Contains(errorMsg(body), "not allowed") {
		t.Fatalf("attach to beta item: %d %s, want 403", res.StatusCode, body)
	}
	// By ref too, and an unknown ref is 403 (no probing) for a restricted key.
	for _, ref := range []string{betaItem.Ref, "beta-999"} {
		res, body = multipartUpload(t, ts.URL+"/api/ingest/attachments/"+ref, restricted, map[string][]byte{"a.png": pngBytes})
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("attach to %s: %d %s, want 403", ref, res.StatusCode, body)
		}
	}

	res, body = capture(t, ts, restricted, "alpha")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("capture alpha: %d %s", res.StatusCode, body)
	}
	var alphaItem dto.Item
	_ = json.Unmarshal(body, &alphaItem)
	res, body = multipartUpload(t, ts.URL+"/api/ingest/attachments/"+alphaItem.ID, restricted, map[string][]byte{"a.png": pngBytes})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("attach to own item: %d %s", res.StatusCode, body)
	}
}

// unauthenticatedRoutes are the only /api routes that take no key at all. The
// route walk fails if one is added without updating this list.
var unauthenticatedRoutes = []string{"GET /setup/status", "POST /setup/complete"}

// TestRouteWalkRestrictedKeyOnlyOnIngest walks every registered /api route:
// each non-ingest, authenticated route must refuse a restricted key with the
// route-class message, whatever it is; each ingest route must not. A route
// added later lands in RoutePatterns automatically, so it cannot skip this.
func TestRouteWalkRestrictedKeyOnlyOnIngest(t *testing.T) {
	st, svc := setup(t)
	if _, err := st.Pool.Exec(context.Background(), `TRUNCATE api_keys, settings CASCADE`); err != nil {
		t.Fatal(err)
	}
	alpha := mkProject(t, st, "alpha")
	restricted := mkKey(t, st, "evals", []string{auth.ScopeIngest}, []uuid.UUID{alpha.ID})
	srv := api.New(st, svc)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)

	patterns := srv.RoutePatterns()
	if len(patterns) < 40 {
		t.Fatalf("only %d routes registered; RoutePatterns is not seeing the mux", len(patterns))
	}
	var refused, ingest int
	var unauth []string
	for _, pat := range patterns {
		method, path, ok := strings.Cut(pat, " ")
		if !ok {
			t.Fatalf("pattern %q has no method; every route must name one", pat)
		}
		if method == http.MethodOptions {
			if !strings.HasPrefix(path, "/ingest/") {
				t.Errorf("OPTIONS route outside ingest: %q", pat)
			}
			continue
		}
		if slices.Contains(unauthenticatedRoutes, pat) {
			unauth = append(unauth, pat)
			continue
		}
		url := ts.URL + "/api" + fillPath(path)
		res, body := doJSON(t, method, url, restricted, map[string]any{}, nil)
		if strings.HasPrefix(path, "/ingest/") {
			ingest++
			if errorMsg(body) == auth.ErrRestrictedRoute || res.StatusCode == http.StatusUnauthorized {
				t.Errorf("%s: restricted key refused on the ingest surface (%d %s)", pat, res.StatusCode, body)
			}
			continue
		}
		refused++
		if res.StatusCode != http.StatusForbidden || errorMsg(body) != auth.ErrRestrictedRoute {
			t.Errorf("%s: restricted key got %d %s, want 403 %q", pat, res.StatusCode, body, auth.ErrRestrictedRoute)
		}
	}
	if !slices.Equal(unauth, unauthenticatedRoutes) {
		t.Errorf("unauthenticated routes = %v, want %v", unauth, unauthenticatedRoutes)
	}
	if ingest != 4 {
		t.Errorf("walked %d ingest routes, want 4 (bug, capture, attachments, projects)", ingest)
	}
	t.Logf("restricted key refused on %d routes, allowed on %d ingest routes", refused, ingest)
}

// fillPath replaces {wildcards} with a plausible value.
func fillPath(path string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(path, '{')
		if i < 0 {
			b.WriteString(path)
			return b.String()
		}
		j := strings.IndexByte(path[i:], '}')
		b.WriteString(path[:i])
		if strings.Contains(path[i:i+j], "slug") {
			b.WriteString("alpha")
		} else {
			b.WriteString(uuid.NewString())
		}
		path = path[i+j+1:]
	}
}

// The explicit cases the route walk also covers, named so a failure reads
// plainly: a read route, a write route, the SSE stream (query-param key), and
// /mcp wired exactly as main wires it.
func TestRestrictedKeyRefusedOffIngest(t *testing.T) {
	ts, st, svc, _ := setupHTTP(t)
	alpha := mkProject(t, st, "alpha")
	restricted := mkKey(t, st, "evals", []string{auth.ScopeIngest}, []uuid.UUID{alpha.ID})

	check := func(what string, res *http.Response, body []byte) {
		t.Helper()
		if res.StatusCode != http.StatusForbidden || errorMsg(body) != auth.ErrRestrictedRoute {
			t.Errorf("%s: %d %s, want 403 %q", what, res.StatusCode, body, auth.ErrRestrictedRoute)
		}
	}
	res, body := doJSON(t, http.MethodGet, ts.URL+"/api/context/alpha", restricted, nil, nil)
	check("read route", res, body)
	res, body = doJSON(t, http.MethodPost, ts.URL+"/api/items", restricted, map[string]any{"project": "alpha", "title": "x"}, nil)
	check("write route", res, body)
	res, body = doJSON(t, http.MethodGet, ts.URL+"/api/stream?api_key="+restricted, "", nil, nil)
	check("stream", res, body)

	mcpSrv := httptest.NewServer(auth.Middleware(st, auth.ScopeWrite)(mcp.NewHandler(st, svc, "test", nil)))
	t.Cleanup(mcpSrv.Close)
	res, body = doJSON(t, http.MethodPost, mcpSrv.URL, restricted, map[string]any{}, nil)
	check("/mcp", res, body)
}

// keygen's refusal is backed by the database: a restricted key with read or
// write scope cannot be stored, and an existing read/write key cannot be
// restricted.
func TestRestrictedKeyScopeRules(t *testing.T) {
	st, _ := setup(t)
	ctx := context.Background()
	alpha := mkProject(t, st, "alpha")
	for _, scopes := range [][]string{{auth.ScopeRead}, {auth.ScopeWrite}, {auth.ScopeIngest, auth.ScopeRead}} {
		_, err := st.CreateAPIKey(ctx, store.CreateAPIKeyParams{
			Name: "bad", KeyHash: auth.HashKey(uuid.NewString()), Scopes: scopes, ProjectIds: []uuid.UUID{alpha.ID},
		})
		if err == nil {
			t.Errorf("stored a restricted key with scopes %v", scopes)
		}
	}
	if _, err := st.CreateAPIKey(ctx, store.CreateAPIKeyParams{
		Name: "empty", KeyHash: auth.HashKey(uuid.NewString()), Scopes: []string{auth.ScopeIngest}, ProjectIds: []uuid.UUID{},
	}); err == nil {
		t.Error("stored a restriction to zero projects")
	}

	rw, err := st.CreateAPIKey(ctx, store.CreateAPIKeyParams{
		Name: "agent", KeyHash: auth.HashKey(uuid.NewString()), Scopes: []string{auth.ScopeRead, auth.ScopeWrite},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.RestrictAPIKey(ctx, st, rw.ID, []string{"alpha"})
	if err == nil || !strings.Contains(err.Error(), "ingest scope") {
		t.Fatalf("restricting a read/write key: err = %v, want the ingest-only explanation", err)
	}
	if _, err := service.RestrictAPIKey(ctx, st, uuid.New(), []string{"alpha"}); err == nil {
		t.Error("restricting a missing key succeeded")
	}
}

// Restrict an existing key in place (secret unchanged), resolve an alias,
// survive a slug rename, then unrestrict.
func TestRestrictExistingKeyAliasAndRename(t *testing.T) {
	ts, st, _, _ := setupHTTP(t)
	ctx := context.Background()
	portfolio, err := st.CreateProject(ctx, store.CreateProjectParams{
		Slug: "portfolio-site", Name: "Portfolio", Aliases: []string{"personal-site", "Personal-Site"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mkProject(t, st, "beta")
	if _, err := st.CreateProject(ctx, store.CreateProjectParams{Slug: "dup-a", Name: "a", Aliases: []string{"shared"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateProject(ctx, store.CreateProjectParams{Slug: "dup-b", Name: "b", Aliases: []string{"Shared"}}); err != nil {
		t.Fatal(err)
	}

	ids, err := service.ResolveKeyProjects(ctx, st, []string{"PERSONAL-SITE", "portfolio-site"})
	if err != nil || len(ids) != 1 || ids[0] != portfolio.ID {
		t.Fatalf("alias resolution = %v, %v; want [portfolio] deduplicated", ids, err)
	}
	if _, err := service.ResolveKeyProjects(ctx, st, []string{"shared"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous alias: err = %v", err)
	}
	if _, err := service.ResolveKeyProjects(ctx, st, []string{"nope"}); err == nil {
		t.Fatal("unknown project resolved")
	}
	if _, err := service.ResolveKeyProjects(ctx, st, nil); err == nil {
		t.Fatal("empty restriction accepted")
	}

	key := mkKey(t, st, "evals", []string{auth.ScopeIngest}, nil)
	stored, err := st.GetAPIKeyByHash(ctx, auth.HashKey(key))
	if err != nil {
		t.Fatal(err)
	}
	row, err := service.RestrictAPIKey(ctx, st, stored.ID, []string{"personal-site"})
	if err != nil {
		t.Fatalf("restrict: %v", err)
	}
	if desc, _ := service.DescribeKeyProjects(ctx, st, row.ProjectIds); desc != "portfolio-site" {
		t.Fatalf("describe = %q", desc)
	}
	// Same secret, now restricted.
	if res, body := capture(t, ts, key, "beta"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("restricted capture into beta: %d %s", res.StatusCode, body)
	}
	if res, body := capture(t, ts, key, "portfolio-site"); res.StatusCode != http.StatusCreated {
		t.Fatalf("restricted capture into portfolio-site: %d %s", res.StatusCode, body)
	}
	// Ingest resolves by exact slug, so an alias in the request is not a way
	// around (or into) the restriction: it is simply unknown.
	if res, body := capture(t, ts, key, "personal-site"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("capture by alias: %d %s, want 403", res.StatusCode, body)
	}

	// Rename the slug: the id-based restriction follows the project.
	if _, err := st.Pool.Exec(ctx, `UPDATE projects SET slug = 'nathanblatter-com' WHERE id = $1`, portfolio.ID); err != nil {
		t.Fatal(err)
	}
	if res, body := capture(t, ts, key, "nathanblatter-com"); res.StatusCode != http.StatusCreated {
		t.Fatalf("capture after rename: %d %s", res.StatusCode, body)
	}
	if desc, _ := service.DescribeKeyProjects(ctx, st, row.ProjectIds); desc != "nathanblatter-com" {
		t.Fatalf("describe after rename = %q", desc)
	}

	if _, err := service.UnrestrictAPIKey(ctx, st, stored.ID); err != nil {
		t.Fatalf("unrestrict: %v", err)
	}
	if res, body := capture(t, ts, key, "beta"); res.StatusCode != http.StatusCreated {
		t.Fatalf("unrestricted capture into beta: %d %s", res.StatusCode, body)
	}
}

// Deleting a restricted key's project leaves the key able to write nowhere,
// even when a new project reuses the slug.
func TestRestrictedKeyDeletedProjectFailsClosed(t *testing.T) {
	ts, st, svc, _ := setupHTTP(t)
	ctx := context.Background()
	gamma := mkProject(t, st, "gamma")
	key := mkKey(t, st, "evals", []string{auth.ScopeIngest}, []uuid.UUID{gamma.ID})

	if err := svc.ArchiveProject(ctx, "gamma"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PurgeProject(ctx, "gamma"); err != nil {
		t.Fatal(err)
	}
	mkProject(t, st, "gamma") // same slug, new id

	if res, body := capture(t, ts, key, "gamma"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("capture into recreated slug: %d %s, want 403", res.StatusCode, body)
	}
	res, body := doJSON(t, http.MethodGet, ts.URL+"/api/ingest/projects", key, nil, nil)
	if res.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("listing after delete: %d %s, want []", res.StatusCode, body)
	}
	if desc, _ := service.DescribeKeyProjects(ctx, st, []uuid.UUID{gamma.ID}); desc != "deleted:"+gamma.ID.String() {
		t.Fatalf("describe = %q", desc)
	}
}
