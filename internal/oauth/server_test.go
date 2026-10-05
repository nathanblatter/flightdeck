package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyPKCE(t *testing.T) {
	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if !VerifyPKCE(verifier, challenge) {
		t.Error("matching verifier rejected")
	}
	if VerifyPKCE(verifier+"x", challenge) {
		t.Error("wrong verifier accepted")
	}
	if VerifyPKCE("short", base64.RawURLEncoding.EncodeToString(func() []byte { s := sha256.Sum256([]byte("short")); return s[:] }())) {
		t.Error("verifier under 43 chars accepted")
	}
	if VerifyPKCE(verifier, "") {
		t.Error("empty challenge accepted")
	}
}

func TestValidRedirectURI(t *testing.T) {
	cases := map[string]bool{
		"https://claude.ai/api/mcp/auth_callback": true,
		"http://localhost:8765/cb":                true,
		"http://127.0.0.1/cb":                     true,
		"http://example.com/cb":                   false,
		"https://example.com/cb#frag":             false,
		"custom://cb":                             false,
		"/relative":                               false,
		"":                                        false,
	}
	for u, want := range cases {
		if got := validRedirectURI(u); got != want {
			t.Errorf("validRedirectURI(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestGrantScope(t *testing.T) {
	held := []string{"read", "write"}
	if s, ok := grantScope("", held); !ok || s != "read write" {
		t.Errorf("no request should grant all held scopes, got %q %v", s, ok)
	}
	if s, ok := grantScope("write ingest", held); !ok || s != "write" {
		t.Errorf("intersection wrong: %q %v", s, ok)
	}
	if _, ok := grantScope("ingest", held); ok {
		t.Error("disjoint request should fail")
	}
}

// The discovery documents need no database: every URL is derived from the
// issuer, and the path-suffixed well-known forms must resolve too.
func TestDiscovery(t *testing.T) {
	s := New(nil, "https://fd.example.com/")
	h := s.Routes()
	get := func(path string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("GET %s: bad JSON: %v", path, err)
		}
		return m
	}
	as := get("/.well-known/oauth-authorization-server")
	if as["issuer"] != "https://fd.example.com" || as["token_endpoint"] != "https://fd.example.com/oauth/token" {
		t.Errorf("unexpected AS metadata: %v", as)
	}
	if get("/.well-known/oauth-authorization-server/mcp")["issuer"] != "https://fd.example.com" {
		t.Error("path-suffixed AS metadata missing")
	}
	pr := get("/.well-known/oauth-protected-resource/mcp")
	if pr["resource"] != "https://fd.example.com/mcp" {
		t.Errorf("unexpected PR metadata: %v", pr)
	}
	if s.ResourceMetadataURL() != "https://fd.example.com/.well-known/oauth-protected-resource/mcp" {
		t.Errorf("ResourceMetadataURL = %q", s.ResourceMetadataURL())
	}
}

// A redirect back to the client must keep the client's own query string and
// drop empty parameters (no dangling state=).
func TestRedirectWith(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/oauth/authorize", nil)
	redirectWith(rec, r, "https://app.example/cb?keep=1", map[string][]string{"code": {"abc"}, "state": {""}})
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusFound || loc != "https://app.example/cb?code=abc&keep=1" {
		t.Errorf("got %d %q", rec.Code, loc)
	}
}

// Registration input is validated before any database call, so these all
// fail fast with nil storage.
func TestRegisterRejectsBadMetadata(t *testing.T) {
	s := New(nil, "https://fd.example.com")
	h := s.Routes()
	for name, body := range map[string]string{
		"no redirect uris": `{"client_name":"x"}`,
		"http redirect":    `{"redirect_uris":["http://evil.example/cb"]}`,
		"bad auth method":  `{"redirect_uris":["https://a.example/cb"],"token_endpoint_auth_method":"private_key_jwt"}`,
		"bad grant":        `{"redirect_uris":["https://a.example/cb"],"grant_types":["implicit"]}`,
		"not json":         `nope`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, rec.Code)
		}
	}
}
