// Package auth implements X-API-Key authentication against the api_keys table.
// Keys are random and high-entropy, so a fast deterministic SHA-256 hash is
// sufficient (and necessary, since we look keys up by hash).
package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"flightdeck/internal/store"
)

const (
	ScopeRead   = "read"
	ScopeWrite  = "write"
	ScopeIngest = "ingest"
)

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
	for _, s := range id.Scopes {
		if subtle.ConstantTimeCompare([]byte(s), []byte(scope)) == 1 {
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
func Middleware(st *store.Store, requiredScope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get("X-API-Key")
			if raw == "" {
				unauthorized(w, "missing X-API-Key")
				return
			}
			key, err := st.GetAPIKeyByHash(r.Context(), HashKey(raw))
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					unauthorized(w, "invalid API key")
					return
				}
				http.Error(w, `{"error":"auth lookup failed"}`, http.StatusInternalServerError)
				return
			}
			id := Identity{Name: key.Name, Scopes: key.Scopes}
			if !id.HasScope(requiredScope) {
				forbidden(w, "key lacks scope: "+requiredScope)
				return
			}
			// Best-effort usage stamp; ignore errors. Debounced to at most one
			// write per key per minute — last_used_at is for humans auditing
			// keys, not a precise counter, and this keeps hot paths read-only.
			if key.LastUsedAt == nil || time.Since(*key.LastUsedAt) > time.Minute {
				_ = st.TouchAPIKey(r.Context(), key.ID)
			}
			setActorRef(r.Context(), id.Name)
			ctx := context.WithValue(r.Context(), ctxKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}

func forbidden(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
