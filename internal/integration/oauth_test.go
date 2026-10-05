package integration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"flightdeck/internal/api"
	"flightdeck/internal/auth"
	"flightdeck/internal/mcp"
	"flightdeck/internal/oauth"
	"flightdeck/internal/store"
)

const (
	publicHost = "fd.example.com"
	privateKey = "fd_test_oauth_key"
)

// hostTransport pins the Host header (the tunnel forwards the public name)
// and optionally a credential, and never follows redirects.
type hostTransport struct {
	host, bearer, apiKey string
}

func (t hostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Host = t.host
	if t.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+t.bearer)
	}
	if t.apiKey != "" {
		req.Header.Set("X-API-Key", t.apiKey)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// setupPublic wires the server exactly as main does with FLIGHTDECK_PUBLIC_URL
// set: a private mux (API + key-authed MCP) and the public mux, split by host.
func setupPublic(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, svc := setup(t)
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, `TRUNCATE api_keys, oauth_clients CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPIKey(ctx, store.CreateAPIKeyParams{
		Name: "phone", KeyHash: auth.HashKey(privateKey), Scopes: []string{auth.ScopeRead, auth.ScopeWrite},
	}); err != nil {
		t.Fatal(err)
	}
	mkProject(t, st, "alpha")

	mcpHandler := mcp.NewHandler(st, svc, "test", nil)
	private := http.NewServeMux()
	private.Handle("/api/", api.New(st, svc).Routes())
	private.Handle("/mcp", auth.Middleware(st, auth.ScopeWrite)(mcpHandler))
	private.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "spa") })

	pub, host, err := oauth.PublicHandler(st, "https://"+publicHost, mcp.NewPublicHandler(st, svc, "test", nil))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(oauth.SplitByHost(host, pub, private))
	t.Cleanup(ts.Close)
	return ts, st
}

func pkcePair() (verifier, challenge string) {
	verifier = strings.Repeat("abcdefgh", 8) // 64 chars
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func postForm(t *testing.T, c *http.Client, u string, form url.Values) (*http.Response, []byte) {
	t.Helper()
	res, err := c.PostForm(u, form)
	if err != nil {
		t.Fatalf("POST %s: %v", u, err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return res, body
}

func listTools(t *testing.T, ts *httptest.Server, tr hostTransport) (int, error) {
	t.Helper()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(context.Background(), &mcpsdk.StreamableClientTransport{
		Endpoint: ts.URL + "/mcp", HTTPClient: &http.Client{Transport: tr},
	}, nil)
	if err != nil {
		return 0, err
	}
	defer sess.Close()
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		return 0, err
	}
	return len(res.Tools), nil
}

// The full connector flow as claude.ai performs it: discover, register,
// send the user to /oauth/authorize, exchange the code with PKCE, call MCP
// with the bearer token, refresh, and observe rotation.
func TestOAuthConnectorFlow(t *testing.T) {
	ts, _ := setupPublic(t)
	pubClient := &http.Client{Transport: hostTransport{host: publicHost}, CheckRedirect: noRedirect}

	// Discovery.
	res, err := pubClient.Get(ts.URL + "/.well-known/oauth-protected-resource/mcp")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("protected resource metadata: %v %v", err, res)
	}
	_ = res.Body.Close()

	// Unauthenticated /mcp must challenge with the metadata URL.
	res, _ = pubClient.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	_ = res.Body.Close()
	if res.StatusCode != 401 || !strings.Contains(res.Header.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource/mcp") {
		t.Fatalf("unauthenticated /mcp: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}

	// Dynamic registration (public client, as Claude registers).
	res, err = pubClient.Post(ts.URL+"/oauth/register", "application/json", strings.NewReader(
		`{"client_name":"Claude","redirect_uris":["https://claude.ai/api/mcp/auth_callback"],"token_endpoint_auth_method":"none"}`))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		ClientID string `json:"client_id"`
		Secret   string `json:"client_secret"`
	}
	if err := json.NewDecoder(res.Body).Decode(&reg); err != nil || res.StatusCode != 201 || reg.ClientID == "" {
		t.Fatalf("register: %d %v %+v", res.StatusCode, err, reg)
	}
	_ = res.Body.Close()
	if reg.Secret != "" {
		t.Error("public client must not receive a secret")
	}

	verifier, challenge := pkcePair()
	authz := url.Values{
		"client_id": {reg.ClientID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"},
		"response_type": {"code"}, "state": {"xyz"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}

	// Login page renders (no credential yet).
	res, err = pubClient.Get(ts.URL + "/oauth/authorize?" + authz.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(page), `name="api_key"`) || !strings.Contains(string(page), "Claude") {
		t.Fatalf("authorize page: %d %s", res.StatusCode, page)
	}

	// Wrong key is refused and re-renders the form.
	bad := cloneValues(authz)
	bad.Set("api_key", "fd_wrong")
	res, body := postForm(t, pubClient, ts.URL+"/oauth/authorize", bad)
	if res.StatusCode != 401 || !strings.Contains(string(body), "not valid") {
		t.Fatalf("bad key: %d %s", res.StatusCode, body)
	}

	// Right key redirects back with a code and the state.
	good := cloneValues(authz)
	good.Set("api_key", privateKey)
	res, _ = postForm(t, pubClient, ts.URL+"/oauth/authorize", good)
	if res.StatusCode != 302 {
		t.Fatalf("authorize: %d", res.StatusCode)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil || loc.Host != "claude.ai" || loc.Query().Get("state") != "xyz" || loc.Query().Get("code") == "" {
		t.Fatalf("redirect: %v %q", err, res.Header.Get("Location"))
	}
	code := loc.Query().Get("code")

	// Exchange: wrong verifier fails, right one succeeds.
	tokenForm := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {reg.ClientID}, "code": {code},
		"redirect_uri": {"https://claude.ai/api/mcp/auth_callback"}, "code_verifier": {strings.Repeat("z", 50)},
	}
	res, body = postForm(t, pubClient, ts.URL+"/oauth/token", tokenForm)
	if res.StatusCode != 400 || !strings.Contains(string(body), "invalid_grant") {
		t.Fatalf("wrong verifier: %d %s", res.StatusCode, body)
	}
	// The code was consumed by that attempt (single use), so a fresh one is needed.
	res, _ = postForm(t, pubClient, ts.URL+"/oauth/authorize", good)
	loc, _ = url.Parse(res.Header.Get("Location"))
	tokenForm.Set("code", loc.Query().Get("code"))
	tokenForm.Set("code_verifier", verifier)
	res, body = postForm(t, pubClient, ts.URL+"/oauth/token", tokenForm)
	var tok struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Type    string `json:"token_type"`
		Scope   string `json:"scope"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || res.StatusCode != 200 || tok.Access == "" || tok.Refresh == "" {
		t.Fatalf("token: %d %s", res.StatusCode, body)
	}
	if tok.Type != "Bearer" || tok.Scope != "read write" {
		t.Errorf("token shape: %+v", tok)
	}
	// Replaying the code must fail.
	res, body = postForm(t, pubClient, ts.URL+"/oauth/token", tokenForm)
	if res.StatusCode != 400 {
		t.Fatalf("code replay: %d %s", res.StatusCode, body)
	}

	// MCP over the public host with the bearer token works.
	n, err := listTools(t, ts, hostTransport{host: publicHost, bearer: tok.Access})
	if err != nil || n == 0 {
		t.Fatalf("mcp with bearer: %v (%d tools)", err, n)
	}
	// A raw API key is refused on the public host even though it is valid privately.
	if _, err := listTools(t, ts, hostTransport{host: publicHost, apiKey: privateKey}); err == nil {
		t.Fatal("api key must not work on the public host")
	}
	if _, err := listTools(t, ts, hostTransport{host: "127.0.0.1", apiKey: privateKey}); err != nil {
		t.Fatalf("api key on the private host: %v", err)
	}
	// Bearer tokens are a first-class credential privately too.
	if _, err := listTools(t, ts, hostTransport{host: "127.0.0.1", bearer: tok.Access}); err != nil {
		t.Fatalf("bearer on the private host: %v", err)
	}

	// Refresh rotates: new pair, old refresh dead, old access dead.
	res, body = postForm(t, pubClient, ts.URL+"/oauth/token", url.Values{
		"grant_type": {"refresh_token"}, "client_id": {reg.ClientID}, "refresh_token": {tok.Refresh},
	})
	var tok2 struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &tok2); err != nil || res.StatusCode != 200 || tok2.Access == "" || tok2.Access == tok.Access {
		t.Fatalf("refresh: %d %s", res.StatusCode, body)
	}
	res, _ = postForm(t, pubClient, ts.URL+"/oauth/token", url.Values{
		"grant_type": {"refresh_token"}, "client_id": {reg.ClientID}, "refresh_token": {tok.Refresh},
	})
	if res.StatusCode != 400 {
		t.Fatalf("refresh replay: %d", res.StatusCode)
	}
	if _, err := listTools(t, ts, hostTransport{host: publicHost, bearer: tok.Access}); err == nil {
		t.Fatal("old access token should be revoked by rotation")
	}
	if n, err := listTools(t, ts, hostTransport{host: publicHost, bearer: tok2.Access}); err != nil || n == 0 {
		t.Fatalf("new access token: %v", err)
	}
}

// Confidential clients (the RFC 7591 default) get a secret and must present
// it at the token endpoint.
func TestOAuthConfidentialClientNeedsSecret(t *testing.T) {
	ts, _ := setupPublic(t)
	pubClient := &http.Client{Transport: hostTransport{host: publicHost}, CheckRedirect: noRedirect}
	res, err := pubClient.Post(ts.URL+"/oauth/register", "application/json", strings.NewReader(
		`{"redirect_uris":["https://claude.ai/api/mcp/auth_callback"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct {
		ClientID string `json:"client_id"`
		Secret   string `json:"client_secret"`
	}
	_ = json.NewDecoder(res.Body).Decode(&reg)
	_ = res.Body.Close()
	if reg.Secret == "" {
		t.Fatal("confidential client should receive a secret")
	}
	verifier, challenge := pkcePair()
	form := url.Values{
		"client_id": {reg.ClientID}, "redirect_uri": {"https://claude.ai/api/mcp/auth_callback"},
		"response_type": {"code"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "api_key": {privateKey},
	}
	res, _ = postForm(t, pubClient, ts.URL+"/oauth/authorize", form)
	loc, _ := url.Parse(res.Header.Get("Location"))
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatalf("no code: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	tf := url.Values{"grant_type": {"authorization_code"}, "client_id": {reg.ClientID}, "code": {code}, "code_verifier": {verifier}}
	res, body := postForm(t, pubClient, ts.URL+"/oauth/token", tf)
	if res.StatusCode != 401 {
		t.Fatalf("no secret: %d %s", res.StatusCode, body)
	}
	// Basic auth carries the secret; the code is still unused because client
	// auth failed before it was consumed.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/oauth/token", strings.NewReader(tf.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(reg.ClientID, reg.Secret)
	res, err = pubClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "access_token") {
		t.Fatalf("with secret: %d %s", res.StatusCode, body)
	}
}

// Nothing but OAuth, discovery and /mcp exists on the public hostname.
func TestPublicHostExposesOnlyOAuth(t *testing.T) {
	ts, _ := setupPublic(t)
	pub := &http.Client{Transport: hostTransport{host: publicHost, apiKey: privateKey}}
	for _, path := range []string{"/", "/api/projects", "/api/setup/status", "/bug-widget.js", "/metrics", "/healthz"} {
		res, err := pub.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != 404 {
			t.Errorf("public %s = %d, want 404", path, res.StatusCode)
		}
	}
	// Same paths privately still work (spot check two).
	priv := &http.Client{Transport: hostTransport{host: "127.0.0.1", apiKey: privateKey}}
	for _, path := range []string{"/", "/api/projects"} {
		res, err := priv.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != 200 {
			t.Errorf("private %s = %d, want 200", path, res.StatusCode)
		}
	}
	// Host matching ignores the port the tunnel may append.
	res, err := (&http.Client{Transport: hostTransport{host: publicHost + ":443"}}).Get(ts.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Errorf("host with port: %d", res.StatusCode)
	}
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
