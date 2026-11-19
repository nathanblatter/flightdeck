// Package auth implements X-API-Key authentication for the private surface
// and holds the Identity that every surface (API keys here, Squawk tokens on
// the public connector, see internal/public) stashes in the request context.
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
	"time"

	"github.com/google/uuid"
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
	// Projects restricts the caller to these project ids. nil means
	// unrestricted (every key minted without a restriction, and every
	// non-key identity such as the Squawk connector). A non-nil slice is a
	// restriction even when empty: an empty list allows nothing.
	Projects []uuid.UUID
}

// ErrRestrictedRoute is the refusal for a project-restricted key on any
// route outside the ingest surface.
const ErrRestrictedRoute = "project-restricted keys are limited to the ingest surface"

// ErrRestrictedScopes explains why a project restriction cannot coexist with
// read or write scope (keygen, keys restrict, and a database CHECK enforce it).
const ErrRestrictedScopes = "a project-restricted key may only hold the ingest scope: project limits are " +
	"enforced on the ingest surface only, and read/write routes and /mcp refuse restricted keys, " +
	"so read or write scope on such a key would never work"

// Restricted reports whether the identity is limited to a project list.
func (id Identity) Restricted() bool { return id.Projects != nil }

// AllowsProject reports whether the identity may act on the project with the
// given id. Unrestricted identities allow every project; a restricted one only
// the ids it lists (so a deleted project, whose id can never match again,
// leaves the key able to write nowhere for it).
func (id Identity) AllowsProject(projectID uuid.UUID) bool {
	if !id.Restricted() {
		return true
	}
	for _, p := range id.Projects {
		if p == projectID {
			return true
		}
	}
	return false
}

// CheckRestrictionScopes validates a scope set for a project-restricted key:
// only ingest is allowed. Returns nil when the set is acceptable.
func CheckRestrictionScopes(scopes []string) error {
	for _, s := range scopes {
		if s != ScopeIngest {
			return errors.New(ErrRestrictedScopes)
		}
	}
	return nil
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

// WithIdentity records id as the authenticated caller on ctx, exactly as
// Middleware does after resolving an API key: FromContext and Actor return it
// and the actor cell (see WithActorRef) is filled so request logging sees the
// name. Other authenticators (the Squawk-verified public connector) use this
// so everything downstream — MCP tools, activity actors — is unchanged.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	setActorRef(ctx, id.Name)
	return context.WithValue(ctx, ctxKey{}, id)
}

// Middleware authenticates the request by API key and enforces that the key
// holds requiredScope. On success the Identity is added to the request
// context.
//
// Route class: requiredScope doubles as the route class, because only the
// ingest surface (/api/ingest/...) is mounted with ScopeIngest. A
// project-restricted key is refused here on every other class (read, write —
// which covers /mcp and the SSE stream) regardless of the scopes it holds:
// project limits are enforced inside the ingest handlers only, so letting a
// restricted key reach a read/write route would silently widen it to every
// project. Keeping the check in this one place means a route added later
// cannot skip it (see the route-walk test in internal/integration). The credential is the X-API-Key header (agents, the SPA) or the
// ?api_key= fallback for EventSource. This is the private (Tailscale) surface
// only; the public hostname never accepts raw keys (see internal/public).
func Middleware(st *store.Store, requiredScope string) func(http.Handler) http.Handler {
	return middleware(st, requiredScope)
}

// keyStore is the slice of the store the middleware needs; an interface so
// the route-class rule can be unit-tested without a database.
type keyStore interface {
	GetAPIKeyByHash(ctx context.Context, keyHash string) (store.ApiKey, error)
	TouchAPIKey(ctx context.Context, id uuid.UUID) error
}

func middleware(st keyStore, requiredScope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, status, msg := authenticate(r, st)
			if status != 0 {
				writeAuthError(w, status, msg)
				return
			}
			if id.Restricted() && requiredScope != ScopeIngest {
				forbidden(w, ErrRestrictedRoute)
				return
			}
			if !id.HasScope(requiredScope) {
				forbidden(w, "key lacks scope: "+requiredScope)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
		})
	}
}

// authenticate resolves the request's API key. A non-zero status is the HTTP
// error to answer with.
func authenticate(r *http.Request, st keyStore) (Identity, int, string) {
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
	id := Identity{Name: key.Name, Scopes: key.Scopes}
	if key.ProjectIds != nil {
		// Copy into a guaranteed non-nil slice: a restriction must never
		// collapse into "unrestricted" (migration 00024 also forbids an
		// empty array, so in practice this always has entries).
		id.Projects = append(make([]uuid.UUID, 0, len(key.ProjectIds)), key.ProjectIds...)
	}
	return id, 0, ""
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
