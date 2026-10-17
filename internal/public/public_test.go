package public

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nathanblatter/squawk/client"

	"flightdeck/internal/auth"
)

const (
	testPublicURL = "https://fd.example.com"
	testServiceID = "svc_test"
	wantMetadata  = testPublicURL + "/.well-known/oauth-protected-resource/mcp"
)

// fakeIssuer is a stand-in Squawk: it signs tokens with its own key and
// serves the JWKS the verifier fetches (same shape as squawk's client tests).
type fakeIssuer struct {
	key *ecdsa.PrivateKey
	srv *httptest.Server
}

func newIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		pt, _ := key.PublicKey.Bytes()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "EC", "crv": "P-256", "kid": "k1", "use": "sig", "alg": "ES256",
			"x": base64.RawURLEncoding.EncodeToString(pt[1:33]),
			"y": base64.RawURLEncoding.EncodeToString(pt[33:]),
		}}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIssuer) token(t *testing.T, aud, scope string) string {
	t.Helper()
	hdr, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "at+jwt", "kid": "k1"})
	now := time.Now()
	body, _ := json.Marshal(client.Claims{
		Issuer: f.srv.URL, Subject: "usr:nathan", Audience: aud, Scope: scope,
		IssuedAt: now.Unix(), Expires: now.Add(time.Hour).Unix(), ID: "jti1",
	})
	input := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, f.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// stub records what the bridged request's context carries.
type stub struct {
	id    auth.Identity
	ok    bool
	actor string
	calls int
}

func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.calls++
	s.id, s.ok = auth.FromContext(r.Context())
	s.actor = auth.ActorRef(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func setup(t *testing.T) (*fakeIssuer, http.Handler, *stub) {
	t.Helper()
	f := newIssuer(t)
	st := &stub{}
	h, host, err := Handler(Config{PublicURL: testPublicURL, Issuer: f.srv.URL, ServiceID: testServiceID}, st)
	if err != nil {
		t.Fatal(err)
	}
	if host != "fd.example.com" {
		t.Fatalf("host = %q", host)
	}
	return f, h, st
}

func call(h http.Handler, method, path, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	// Outer logging middleware seeds the actor cell before auth runs.
	req = req.WithContext(auth.WithActorRef(context.Background()))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMissingTokenChallenges(t *testing.T) {
	_, h, st := setup(t)
	for _, path := range []string{"/mcp", "/mcp/"} {
		rec := call(h, http.MethodPost, path, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s no token: %d", path, rec.Code)
		}
		if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer ") || !strings.Contains(got, `resource_metadata="`+wantMetadata+`"`) {
			t.Fatalf("%s challenge: %q", path, got)
		}
	}
	if st.calls != 0 {
		t.Fatal("handler reached without a token")
	}
}

func TestWrongAudienceRejected(t *testing.T) {
	f, h, st := setup(t)
	rec := call(h, http.MethodPost, "/mcp", f.token(t, "svc_other", "read write"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong aud: %d", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, `resource_metadata="`+wantMetadata+`"`) || !strings.Contains(got, `error="invalid_token"`) {
		t.Fatalf("challenge: %q", got)
	}
	if st.calls != 0 {
		t.Fatal("handler reached with wrong audience")
	}
}

func TestMissingWriteScopeForbidden(t *testing.T) {
	f, h, st := setup(t)
	rec := call(h, http.MethodPost, "/mcp", f.token(t, testServiceID, "read"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read-only token: %d", rec.Code)
	}
	if st.calls != 0 {
		t.Fatal("handler reached without write scope")
	}
}

func TestValidTokenBridgesIdentity(t *testing.T) {
	f, h, st := setup(t)
	rec := call(h, http.MethodPost, "/mcp", f.token(t, testServiceID, "read write"))
	if rec.Code != http.StatusNoContent || st.calls != 1 {
		t.Fatalf("valid token: %d calls=%d", rec.Code, st.calls)
	}
	if !st.ok || st.id.Name != ActorName {
		t.Fatalf("identity: %+v ok=%v", st.id, st.ok)
	}
	if !st.id.HasScope(auth.ScopeRead) || !st.id.HasScope(auth.ScopeWrite) || len(st.id.Scopes) != 2 {
		t.Fatalf("scopes: %v", st.id.Scopes)
	}
	if st.actor != ActorName {
		t.Fatalf("actor cell: %q", st.actor)
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("challenge header on a success")
	}
}

func TestMetadataDocument(t *testing.T) {
	f, h, _ := setup(t)
	rec := call(h, http.MethodGet, "/.well-known/oauth-protected-resource/mcp", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata: %d", rec.Code)
	}
	var md struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
		Scopes   []string `json:"scopes_supported"`
		Name     string   `json:"resource_name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &md); err != nil {
		t.Fatal(err)
	}
	if md.Resource != testPublicURL+"/mcp" || len(md.Servers) != 1 || md.Servers[0] != f.srv.URL || md.Name != resourceName {
		t.Fatalf("metadata: %+v", md)
	}
	if strings.Join(md.Scopes, " ") != "read write" {
		t.Fatalf("scopes: %v", md.Scopes)
	}
}

func TestEverythingElseIs404(t *testing.T) {
	f, h, _ := setup(t)
	tok := f.token(t, testServiceID, "read write")
	for _, path := range []string{"/", "/api/projects", "/healthz", "/metrics", "/bug-widget.js",
		"/oauth/authorize", "/oauth/token", "/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource"} {
		if rec := call(h, http.MethodGet, path, tok); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, rec.Code)
		}
	}
}

func TestHandlerConfigValidation(t *testing.T) {
	bad := []Config{
		{PublicURL: "http://fd.example.com", Issuer: "https://sq", ServiceID: "svc"},
		{PublicURL: "https://fd.example.com/mcp", Issuer: "https://sq", ServiceID: "svc"},
		{PublicURL: "https://fd.example.com", Issuer: "", ServiceID: "svc"},
		{PublicURL: "https://fd.example.com", Issuer: "https://sq", ServiceID: ""},
	}
	for _, cfg := range bad {
		if _, _, err := Handler(cfg, http.NotFoundHandler()); err == nil {
			t.Errorf("%+v: expected error", cfg)
		}
	}
}

func TestSplitByHost(t *testing.T) {
	pub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(201) })
	priv := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202) })
	h := SplitByHost("fd.example.com", pub, priv)
	cases := map[string]int{
		"fd.example.com":     201,
		"FD.Example.com:443": 201,
		"100.79.61.79:4300":  202,
		"localhost":          202,
	}
	for host, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("host %q: %d want %d", host, rec.Code, want)
		}
	}
}
