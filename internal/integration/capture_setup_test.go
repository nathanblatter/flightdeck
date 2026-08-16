package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"flightdeck/internal/api"
	"flightdeck/internal/auth"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

// setupHTTP wires the real /api routes over the test DB and returns the server
// plus a raw ingest-scoped key. Also clears api_keys/settings, which the shared
// setup() truncate leaves alone.
func setupHTTP(t *testing.T) (*httptest.Server, *store.Store, *service.Service, string) {
	st, svc := setup(t)
	if _, err := st.Pool.Exec(context.Background(), `TRUNCATE api_keys, settings`); err != nil {
		t.Fatalf("truncate keys/settings: %v", err)
	}
	raw := "fd_test_ingest_key"
	if _, err := st.CreateAPIKey(context.Background(), store.CreateAPIKeyParams{
		Name: "test-ingest", KeyHash: auth.HashKey(raw), Scopes: []string{auth.ScopeIngest},
	}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	srv := api.New(st, svc)
	mux := http.NewServeMux()
	mux.Handle("/api/", srv.Routes())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, st, svc, raw
}

func doJSON(t *testing.T, method, url, key string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp, out.Bytes()
}

func TestIngestCapture(t *testing.T) {
	ts, st, _, key := setupHTTP(t)
	mkProject(t, st, "workproj")

	resp, body := doJSON(t, "POST", ts.URL+"/api/ingest/capture", key,
		map[string]any{"project": "workproj", "title": "ship the demo", "type": "task"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("capture: status %d: %s", resp.StatusCode, body)
	}
	var item struct {
		Type, Source, Priority, Title string
		Tags                          []string
	}
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if item.Type != "task" || item.Source != "capture" || item.Priority != "med" ||
		item.Title != "ship the demo" || len(item.Tags) != 1 || item.Tags[0] != "captured" {
		t.Fatalf("unexpected item: %+v", item)
	}

	// defaults: type omitted → task; bad type → 400; missing title → 400; bad project → 404
	if resp, _ := doJSON(t, "POST", ts.URL+"/api/ingest/capture", key,
		map[string]any{"project": "workproj", "title": "x"}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("default type: status %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, "POST", ts.URL+"/api/ingest/capture", key,
		map[string]any{"project": "workproj", "title": "x", "type": "epic"}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad type: status %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, "POST", ts.URL+"/api/ingest/capture", key,
		map[string]any{"project": "workproj"}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing title: status %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, "POST", ts.URL+"/api/ingest/capture", key,
		map[string]any{"project": "nope", "title": "x"}, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad project: status %d", resp.StatusCode)
	}
}

func TestIngestProjectsListsActiveSlugs(t *testing.T) {
	ts, st, _, key := setupHTTP(t)
	mkProject(t, st, "alpha")
	mkProject(t, st, "paused-proj")
	paused := "paused"
	if _, err := st.UpdateProject(context.Background(), store.UpdateProjectParams{Slug: "paused-proj", Status: &paused}); err != nil {
		t.Fatalf("pause project: %v", err)
	}

	resp, body := doJSON(t, "GET", ts.URL+"/api/ingest/projects", key, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("projects: status %d: %s", resp.StatusCode, body)
	}
	var slugs []string
	if err := json.Unmarshal(body, &slugs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(slugs) != 1 || slugs[0] != "alpha" {
		t.Fatalf("expected [alpha], got %v", slugs)
	}
}

func TestSetupFlow(t *testing.T) {
	st, svc := setup(t)
	if _, err := st.Pool.Exec(context.Background(), `TRUNCATE api_keys, settings`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	svc.ReloadSettings(context.Background())

	srv := api.New(st, svc)
	srv.Version = "test"
	srv.SetupToken = "fdsetup_secret"
	mux := http.NewServeMux()
	mux.Handle("/api/", srv.Routes())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// fresh instance: status reports incomplete, unauthenticated
	resp, body := doJSON(t, "GET", ts.URL+"/api/setup/status", "", nil, nil)
	var status struct {
		SetupComplete bool   `json:"setup_complete"`
		InstanceName  string `json:"instance_name"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &status) != nil || status.SetupComplete {
		t.Fatalf("expected incomplete setup, got %d %s", resp.StatusCode, body)
	}

	completeBody := map[string]any{
		"instance_name": "Flightdeck @ Test",
		"flags":         map[string]bool{"usage_analytics": false},
		"keys": []map[string]any{
			{"name": "personal", "scopes": []string{"read", "write"}},
			{"name": "ingest", "scopes": []string{"ingest"}},
		},
	}

	// wrong token rejected
	if resp, _ := doJSON(t, "POST", ts.URL+"/api/setup/complete", "", completeBody,
		map[string]string{"X-Setup-Token": "wrong"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: status %d", resp.StatusCode)
	}

	// correct token mints keys and completes setup
	resp, body = doJSON(t, "POST", ts.URL+"/api/setup/complete", "", completeBody,
		map[string]string{"X-Setup-Token": "fdsetup_secret"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete: status %d: %s", resp.StatusCode, body)
	}
	var minted struct {
		Keys []struct{ Name, Key string }
	}
	if err := json.Unmarshal(body, &minted); err != nil || len(minted.Keys) != 2 {
		t.Fatalf("expected 2 minted keys: %v %s", err, body)
	}
	for _, k := range minted.Keys {
		if len(k.Key) < 10 {
			t.Fatalf("key %q looks unminted: %q", k.Name, k.Key)
		}
	}

	// status now complete with the instance name; second complete → 410
	_, body = doJSON(t, "GET", ts.URL+"/api/setup/status", "", nil, nil)
	if json.Unmarshal(body, &status) != nil || !status.SetupComplete || status.InstanceName != "Flightdeck @ Test" {
		t.Fatalf("expected complete setup with name, got %s", body)
	}
	if resp, _ := doJSON(t, "POST", ts.URL+"/api/setup/complete", "", completeBody,
		map[string]string{"X-Setup-Token": "fdsetup_secret"}); resp.StatusCode != http.StatusGone {
		t.Fatalf("re-complete: status %d", resp.StatusCode)
	}

	// minted personal key works against a read route; flags applied
	personal := minted.Keys[0].Key
	if resp, _ := doJSON(t, "GET", ts.URL+"/api/projects", personal, nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("minted key rejected: status %d", resp.StatusCode)
	}
	if svc.Flag(service.FlagUsageAnalytics) {
		t.Fatal("usage_analytics flag should be off")
	}
	if !svc.Flag(service.FlagBugWidget) {
		t.Fatal("unset bug_widget flag should default on")
	}
}
