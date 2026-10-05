// Package auth implements X-API-Key and OAuth bearer authentication. Keys are
// looked up in api_keys; bearer tokens in oauth_tokens, each resolving to the
// key it was minted from.
// Keys are random and high-entropy, so a fast deterministic SHA-256 hash is
// sufficient (and necessary, since we look keys up by hash).
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"flightdeck/internal/store"
)

const (
	ScopeRead   = "read"
	ScopeWrite  = "write"
	ScopeIngest = "ingest"
)

// NewRawKey generates a high-entropy random key with the given prefix
// ("fd_" for API keys, "fdsetup_" for one-time setup tokens). Shared by the
// keygen CLI and the setup wizard endpoint.
func NewRawKey(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashKey returns the hex SHA-256 of a raw API key, as stored in key_hash.
func HashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

type ctxKey struct{}

// Identity is the authenticated caller, stashed in the request context.
type Identity struct {
	Name   string
	Scopes []string
}

func (id Identity) HasScope(scope string) bool {
	// Plain comparison: scope names are public constants, not secrets.
	for _, s := range id.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// FromContext returns the authenticated identity, if any.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// Actor returns the caller name for activity logging, falling back to "unknown".
func Actor(ctx context.Context) string {
	if id, ok := FromContext(ctx); ok && id.Name != "" {
		return id.Name
	}
	return "unknown"
}

// actorRef is a mutable cell so an outer logging middleware (which runs before
// auth resolves the key) can read the actor name that auth fills in below it.
type actorRef struct{ name string }
type actorRefKey struct{}

// WithActorRef seeds a context with an empty actor cell. The auth Middleware
// fills it once the key resolves; outer middleware reads it via ActorRef after
// the request has been served.
func WithActorRef(ctx context.Context) context.Context {
	return context.WithValue(ctx, actorRefKey{}, &actorRef{})
}

func setActorRef(ctx context.Context, name string) {
	if ref, ok := ctx.Value(actorRefKey{}).(*actorRef); ok {
		ref.name = name
	}
}

// ActorRef returns the actor recorded in the context's cell, or "" if none.
func ActorRef(ctx context.Context) string {
	if ref, ok := ctx.Value(actorRefKey{}).(*actorRef); ok {
		return ref.name
	}
	return ""
}

// Middleware authenticates the request and enforces that the identity holds
// requiredScope. On success the Identity is added to the request context.
//
// Two credentials are accepted: the X-API-Key header (agents, the SPA, the
// ?api_key= fallback for EventSource) and an OAuth bearer token minted by the
// public connector flow, which resolves to the api_keys row it was issued for.
func Middleware(st *store.Store, requiredScope string) func(http.Handler) http.Handler {
	return middleware(st, requiredScope, true, "")
}

// PublicMiddleware is the posture for the internet-facing hostname: bearer
// tokens only. A raw API key must never be usable from the public side, so a
// leaked key cannot be replayed through the tunnel. The 401 advertises the
// protected-resource metadata so MCP clients can discover the authorization
// server (RFC 9728 / MCP authorization spec).
func PublicMiddleware(st *store.Store, requiredScope, resourceMetadataURL string) func(http.Handler) http.Handler {
	return middleware(st, requiredScope, false, resourceMetadataURL)
}

func middleware(st *store.Store, requiredScope string, allowAPIKey bool, resourceMetadataURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if resourceMetadataURL != "" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resourceMetadataURL+`"`)
			}
			id, status, msg := authenticate(r, st, allowAPIKey)
			if status != 0 {
				writeAuthError(w, status, msg)
				return
			}
			if !id.HasScope(requiredScope) {
				forbidden(w, "key lacks scope: "+requiredScope)
				return
			}
			// Success: the challenge header only belongs on rejections.
			w.Header().Del("WWW-Authenticate")
			setActorRef(r.Context(), id.Name)
			ctx := context.WithValue(r.Context(), ctxKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// authenticate resolves the request's credential. A non-zero status is the
// HTTP error to answer with.
func authenticate(r *http.Request, st *store.Store, allowAPIKey bool) (Identity, int, string) {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return bearerIdentity(r, st, strings.TrimSpace(tok))
	}
	if !allowAPIKey {
		return Identity{}, http.StatusUnauthorized, "missing bearer token"
	}
	raw := r.Header.Get("X-API-Key")
	if raw == "" {
		// Fallback for browser EventSource, which can't set request
		// headers — used by the SSE stream. The request logger records
		// only the path, not the query string, so the key isn't logged.
		raw = r.URL.Query().Get("api_key")
	}
	if raw == "" {
		return Identity{}, http.StatusUnauthorized, "missing X-API-Key"
	}
	key, err := st.GetAPIKeyByHash(r.Context(), HashKey(raw))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Identity{}, http.StatusUnauthorized, "invalid API key"
		}
		return Identity{}, http.StatusInternalServerError, "auth lookup failed"
	}
	// Best-effort usage stamp; ignore errors. Debounced to at most one
	// write per key per minute — last_used_at is for humans auditing
	// keys, not a precise counter, and this keeps hot paths read-only.
	if key.LastUsedAt == nil || time.Since(*key.LastUsedAt) > time.Minute {
		_ = st.TouchAPIKey(r.Context(), key.ID)
	}
	return Identity{Name: key.Name, Scopes: key.Scopes}, 0, ""
}

func bearerIdentity(r *http.Request, st *store.Store, tok string) (Identity, int, string) {
	if tok == "" {
		return Identity{}, http.StatusUnauthorized, "missing bearer token"
	}
	row, err := st.GetOAuthTokenByAccessHash(r.Context(), HashKey(tok))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Identity{}, http.StatusUnauthorized, "invalid or expired token"
		}
		return Identity{}, http.StatusInternalServerError, "auth lookup failed"
	}
	if row.LastUsedAt == nil || time.Since(*row.LastUsedAt) > time.Minute {
		_ = st.TouchOAuthToken(r.Context(), row.ID)
	}
	// The token acts as the key it was minted from: same actor name on
	// activity, same revocation. Scopes are what the grant narrowed the key
	// to (a client may ask for less than the key holds).
	scopes := strings.Fields(row.Scope)
	if len(scopes) == 0 {
		scopes = row.KeyScopes
	}
	return Identity{Name: row.KeyName, Scopes: scopes}, 0, ""
}

func forbidden(w http.ResponseWriter, msg string) {
	writeAuthError(w, http.StatusForbidden, msg)
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// EqualHash compares two key hashes in constant time.
func EqualHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
