// Package oauth is the OAuth 2.1 authorization server behind the public MCP
// connector surface. claude.ai and the Claude mobile app add an MCP server by
// URL and can only authenticate with OAuth — no custom headers — so this is
// what lets a phone reach flightdeck.
//
// It is deliberately small: opaque tokens stored hashed, PKCE S256 only,
// dynamic client registration (RFC 7591) because the clients self-register,
// and a login page whose credential is an existing API key. Every token
// resolves to the api_keys row it was minted from, so identity, scopes and
// revocation stay in the one place they already live.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"flightdeck/internal/auth"
	"flightdeck/internal/ratelimit"
	"flightdeck/internal/store"
)

const (
	codeTTL    = 10 * time.Minute
	accessTTL  = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	// resourcePath is the protected resource the tokens are for.
	resourcePath = "/mcp"
)

// Server serves the discovery documents and the OAuth endpoints. Issuer is
// the public base URL (scheme + host, no trailing slash), which is also the
// prefix of every endpoint it advertises.
type Server struct {
	St     *store.Store
	Issuer string

	// loginLimiter throttles credential guesses on the authorize form and
	// token endpoint; both are reachable from the internet by design.
	loginLimiter *ratelimit.IPLimiter
}

func New(st *store.Store, issuer string) *Server {
	return &Server{
		St:           st,
		Issuer:       strings.TrimRight(issuer, "/"),
		loginLimiter: ratelimit.New(0.5, 10), // 10 tries, then one every 2s per IP
	}
}

// ResourceMetadataURL is what the 401 challenge on /mcp points at.
func (s *Server) ResourceMetadataURL() string {
	return s.Issuer + "/.well-known/oauth-protected-resource" + resourcePath
}

// Routes mounts the discovery documents and OAuth endpoints. The well-known
// paths accept a resource suffix (".../oauth-protected-resource/mcp") because
// clients try the path-aware form first.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.authServerMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/{rest...}", s.authServerMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.protectedResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/{rest...}", s.protectedResourceMetadata)
	mux.HandleFunc("POST /oauth/register", s.register)
	mux.HandleFunc("GET /oauth/authorize", s.authorizeForm)
	limited := func(h http.HandlerFunc) http.Handler {
		return s.loginLimiter.Middleware(func(w http.ResponseWriter) {
			writeOAuthError(w, http.StatusTooManyRequests, "slow_down", "too many attempts, try again shortly")
		}, h)
	}
	mux.Handle("POST /oauth/authorize", limited(s.authorizeSubmit))
	mux.Handle("POST /oauth/token", limited(s.token))
	return mux
}

// --- discovery ---------------------------------------------------------------

func (s *Server) authServerMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.Issuer,
		"authorization_endpoint":                s.Issuer + "/oauth/authorize",
		"token_endpoint":                        s.Issuer + "/oauth/token",
		"registration_endpoint":                 s.Issuer + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"response_modes_supported":              []string{"query"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                      []string{auth.ScopeRead, auth.ScopeWrite},
	})
}

func (s *Server) protectedResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.Issuer + resourcePath,
		"authorization_servers":    []string{s.Issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{auth.ScopeRead, auth.ScopeWrite},
		"resource_name":            "flightdeck",
	})
}

// --- dynamic client registration (RFC 7591) ----------------------------------

type registerReq struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "body must be JSON")
		return
	}
	if len(req.RedirectURIs) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirectURI(u) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URIs must be absolute https URLs (http only for localhost)")
			return
		}
	}
	method := req.TokenEndpointAuthMethod
	if method == "" {
		method = "client_secret_basic" // the RFC 7591 default
	}
	switch method {
	case "none", "client_secret_post", "client_secret_basic":
	default:
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	for _, g := range req.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant_type "+g)
			return
		}
	}
	for _, rt := range req.ResponseTypes {
		if rt != "code" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported response_type "+rt)
			return
		}
	}

	id, err := randomToken("fdc_")
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "entropy unavailable")
		return
	}
	var secret string
	var secretHash *string
	if method != "none" {
		if secret, err = randomToken("fdcs_"); err != nil {
			writeOAuthError(w, http.StatusInternalServerError, "server_error", "entropy unavailable")
			return
		}
		h := auth.HashKey(secret)
		secretHash = &h
	}
	name := strings.TrimSpace(req.ClientName)
	if len(name) > 200 {
		name = name[:200]
	}
	c, err := s.St.CreateOAuthClient(r.Context(), store.CreateOAuthClientParams{
		ID: id, SecretHash: secretHash, Name: name, RedirectUris: req.RedirectURIs, AuthMethod: method,
	})
	if err != nil {
		log.Printf("oauth: register client: %v", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not store client")
		return
	}
	resp := map[string]any{
		"client_id":                  c.ID,
		"client_id_issued_at":        c.CreatedAt.Unix(),
		"client_name":                c.Name,
		"redirect_uris":              c.RedirectUris,
		"token_endpoint_auth_method": c.AuthMethod,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	}
	if secret != "" {
		resp["client_secret"] = secret
		resp["client_secret_expires_at"] = 0
	}
	writeJSON(w, http.StatusCreated, resp)
}

func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	return false
}

// --- authorization endpoint ----------------------------------------------------

// authzParams is the request as parsed from the query (GET) or the form (POST
// echoes them back as hidden fields).
type authzParams struct {
	ClientID, RedirectURI, ResponseType, State, Scope, Challenge, Method, Resource string
}

func parseAuthz(v url.Values) authzParams {
	return authzParams{
		ClientID:     v.Get("client_id"),
		RedirectURI:  v.Get("redirect_uri"),
		ResponseType: v.Get("response_type"),
		State:        v.Get("state"),
		Scope:        v.Get("scope"),
		Challenge:    v.Get("code_challenge"),
		Method:       v.Get("code_challenge_method"),
		Resource:     v.Get("resource"),
	}
}

// loadClient validates the two parameters that must be right before anything
// may be redirected anywhere (RFC 6749 §4.1.2.1): the client and its
// redirect URI. Errors here render a page; they never bounce the user.
func (s *Server) loadClient(ctx context.Context, p authzParams) (store.OauthClient, string) {
	if p.ClientID == "" {
		return store.OauthClient{}, "missing client_id"
	}
	c, err := s.St.GetOAuthClient(ctx, p.ClientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.OauthClient{}, "unknown client_id"
		}
		return store.OauthClient{}, "client lookup failed"
	}
	if p.RedirectURI == "" {
		return store.OauthClient{}, "missing redirect_uri"
	}
	for _, u := range c.RedirectUris {
		if u == p.RedirectURI {
			return c, ""
		}
	}
	return store.OauthClient{}, "redirect_uri is not registered for this client"
}

// validateAuthz checks the rest; failures redirect back with an error.
func validateAuthz(p authzParams) (code, desc string) {
	if p.ResponseType != "code" {
		return "unsupported_response_type", "response_type must be code"
	}
	if p.Challenge == "" || p.Method != "S256" {
		return "invalid_request", "PKCE S256 code_challenge is required"
	}
	return "", ""
}

func (s *Server) authorizeForm(w http.ResponseWriter, r *http.Request) {
	p := parseAuthz(r.URL.Query())
	c, msg := s.loadClient(r.Context(), p)
	if msg != "" {
		renderPage(w, http.StatusBadRequest, pageData{Title: "Cannot continue", Error: msg})
		return
	}
	if code, desc := validateAuthz(p); code != "" {
		redirectError(w, r, p, code, desc)
		return
	}
	renderPage(w, http.StatusOK, pageData{Title: "Connect to flightdeck", Client: clientLabel(c), Params: p})
}

func (s *Server) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		renderPage(w, http.StatusBadRequest, pageData{Title: "Cannot continue", Error: "malformed form"})
		return
	}
	p := parseAuthz(r.PostForm)
	c, msg := s.loadClient(r.Context(), p)
	if msg != "" {
		renderPage(w, http.StatusBadRequest, pageData{Title: "Cannot continue", Error: msg})
		return
	}
	if code, desc := validateAuthz(p); code != "" {
		redirectError(w, r, p, code, desc)
		return
	}
	retry := func(msg string) {
		renderPage(w, http.StatusUnauthorized, pageData{Title: "Connect to flightdeck", Client: clientLabel(c), Params: p, Error: msg})
	}
	raw := strings.TrimSpace(r.PostForm.Get("api_key"))
	if raw == "" {
		retry("Paste an API key to continue.")
		return
	}
	key, err := s.St.GetAPIKeyByHash(r.Context(), auth.HashKey(raw))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			retry("That API key is not valid.")
			return
		}
		retry("Key lookup failed; try again.")
		return
	}
	scope, ok := grantScope(p.Scope, key.Scopes)
	if !ok {
		redirectError(w, r, p, "invalid_scope", "this key does not hold the requested scope")
		return
	}
	code, err := randomToken("fdac_")
	if err != nil {
		retry("Could not issue a code; try again.")
		return
	}
	err = s.St.CreateOAuthCode(r.Context(), store.CreateOAuthCodeParams{
		CodeHash: auth.HashKey(code), ClientID: c.ID, ApiKeyID: key.ID,
		RedirectUri: p.RedirectURI, CodeChallenge: p.Challenge, Scope: scope,
		ExpiresAt: time.Now().Add(codeTTL),
	})
	if err != nil {
		log.Printf("oauth: store code: %v", err)
		retry("Could not issue a code; try again.")
		return
	}
	log.Printf("oauth: authorized client %q as key %q", clientLabel(c), key.Name)
	redirectWith(w, r, p.RedirectURI, url.Values{"code": {code}, "state": {p.State}})
}

// grantScope intersects what the client asked for with what the key holds.
// No request means everything the key has.
func grantScope(requested string, held []string) (string, bool) {
	if strings.TrimSpace(requested) == "" {
		return strings.Join(held, " "), true
	}
	var out []string
	for _, want := range strings.Fields(requested) {
		for _, h := range held {
			if h == want {
				out = append(out, h)
				break
			}
		}
	}
	if len(out) == 0 {
		return "", false
	}
	return strings.Join(out, " "), true
}

func clientLabel(c store.OauthClient) string {
	if c.Name != "" {
		return c.Name
	}
	return c.ID
}

func redirectError(w http.ResponseWriter, r *http.Request, p authzParams, code, desc string) {
	redirectWith(w, r, p.RedirectURI, url.Values{"error": {code}, "error_description": {desc}, "state": {p.State}})
}

// redirectWith sends the client back to its (already validated) redirect URI
// with params merged into the existing query. Empty values are dropped.
func redirectWith(w http.ResponseWriter, r *http.Request, redirectURI string, params url.Values) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "bad redirect", http.StatusBadRequest)
		return
	}
	q := u.Query()
	for k, vs := range params {
		for _, v := range vs {
			if v != "" {
				q.Set(k, v)
			}
		}
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// --- token endpoint ------------------------------------------------------------

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form")
		return
	}
	c, ok := s.authenticateClient(w, r)
	if !ok {
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r, c)
	case "refresh_token":
		s.refresh(w, r, c)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

// authenticateClient resolves client_id (+ secret for confidential clients)
// from the form or HTTP Basic auth. It writes the error itself.
func (s *Server) authenticateClient(w http.ResponseWriter, r *http.Request) (store.OauthClient, bool) {
	id, secret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	if bu, bp, ok := r.BasicAuth(); ok {
		id, secret = bu, bp
	}
	if id == "" {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client_id is required")
		return store.OauthClient{}, false
	}
	c, err := s.St.GetOAuthClient(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
			return store.OauthClient{}, false
		}
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "client lookup failed")
		return store.OauthClient{}, false
	}
	if c.AuthMethod != "none" {
		if c.SecretHash == nil || secret == "" || !auth.EqualHash(auth.HashKey(secret), *c.SecretHash) {
			writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
			return store.OauthClient{}, false
		}
	}
	return c, true
}

func (s *Server) exchangeCode(w http.ResponseWriter, r *http.Request, c store.OauthClient) {
	code, verifier, redirectURI := r.PostForm.Get("code"), r.PostForm.Get("code_verifier"), r.PostForm.Get("redirect_uri")
	if code == "" || verifier == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code and code_verifier are required")
		return
	}
	row, err := s.St.ConsumeOAuthCode(r.Context(), auth.HashKey(code))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code is invalid, expired, or already used")
			return
		}
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "code lookup failed")
		return
	}
	if row.ClientID != c.ID || (redirectURI != "" && redirectURI != row.RedirectUri) || !VerifyPKCE(verifier, row.CodeChallenge) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code does not match this client, redirect_uri, or code_verifier")
		return
	}
	s.mint(w, r, c.ID, row.ApiKeyID, row.Scope)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request, c store.OauthClient) {
	rt := r.PostForm.Get("refresh_token")
	if rt == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	row, err := s.St.ConsumeOAuthRefresh(r.Context(), auth.HashKey(rt))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token is invalid, expired, or already used")
			return
		}
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "token lookup failed")
		return
	}
	if row.ClientID != c.ID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh token belongs to another client")
		return
	}
	s.mint(w, r, c.ID, row.ApiKeyID, row.Scope)
}

// mint issues a fresh access/refresh pair bound to the key.
func (s *Server) mint(w http.ResponseWriter, r *http.Request, clientID string, keyID uuid.UUID, scope string) {
	access, err1 := randomToken("fdat_")
	refresh, err2 := randomToken("fdrt_")
	if err1 != nil || err2 != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "entropy unavailable")
		return
	}
	now := time.Now()
	_, err := s.St.CreateOAuthToken(r.Context(), store.CreateOAuthTokenParams{
		ClientID: clientID, ApiKeyID: keyID,
		AccessHash: auth.HashKey(access), RefreshHash: auth.HashKey(refresh), Scope: scope,
		AccessExpiresAt: now.Add(accessTTL), RefreshExpiresAt: now.Add(refreshTTL),
	})
	if err != nil {
		log.Printf("oauth: store token: %v", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not store token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         scope,
	})
}

// --- helpers -------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeOAuthError is the RFC 6749 §5.2 error shape.
func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

type pageData struct {
	Title  string
	Client string
	Error  string
	Params authzParams
}

var page = template.Must(template.New("authorize").Parse(authorizeHTML))

func renderPage(w http.ResponseWriter, status int, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	if err := page.Execute(w, d); err != nil {
		log.Printf("oauth: render: %v", err)
	}
}
