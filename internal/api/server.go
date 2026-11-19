// Package api implements the HTTP JSON API mounted under /api.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5"

	"flightdeck/internal/auth"
	"flightdeck/internal/ratelimit"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
	"flightdeck/internal/update"
)

type Server struct {
	St  *store.Store
	Svc *service.Service

	// Version is reported by GET /setup/status (and set from main).
	Version string
	// Upd surfaces "a newer release exists" on /setup/status for the SPA
	// banner. Nil (disabled or tests) reports no update.
	Upd *update.Checker
	// SetupToken authenticates the one-time first-run wizard; empty once an
	// instance is set up (see setup.go).
	SetupToken string

	setupMu   sync.Mutex  // serializes setup completion attempts
	setupDone atomic.Bool // cached "setup finished" — once true, always true
}

func New(st *store.Store, svc *service.Service) *Server {
	return &Server{St: st, Svc: svc}
}

// Routes returns the /api handler with per-route API-key scope enforcement.
func (s *Server) Routes() http.Handler {
	h, _ := s.buildRoutes()
	return h
}

// RoutePatterns returns every pattern Routes registers ("METHOD /path",
// relative to /api), in registration order. Tests walk it to prove that every
// non-ingest route refuses a project-restricted key.
func (s *Server) RoutePatterns() []string {
	_, patterns := s.buildRoutes()
	return patterns
}

// buildRoutes registers the /api routes. Every route MUST go through handle
// (never the mux directly) so RoutePatterns, and the route-walk test built on
// it, see it.
func (s *Server) buildRoutes() (http.Handler, []string) {
	var patterns []string
	mux := http.NewServeMux()
	handle := func(pattern string, h http.Handler) {
		patterns = append(patterns, pattern)
		mux.Handle(pattern, h)
	}

	read := func(h http.HandlerFunc) http.Handler { return auth.Middleware(s.St, auth.ScopeRead)(h) }
	write := func(h http.HandlerFunc) http.Handler { return auth.Middleware(s.St, auth.ScopeWrite)(h) }
	ingest := func(h http.HandlerFunc) http.Handler { return auth.Middleware(s.St, auth.ScopeIngest)(h) }

	// projects
	handle("GET /projects", read(s.listProjects))
	handle("POST /projects", write(s.createProject))
	handle("GET /projects/{slug}", read(s.getProject))
	handle("PATCH /projects/{slug}", write(s.patchProject))
	handle("DELETE /projects/{slug}", write(s.deleteProject))

	// sharing
	handle("GET /shares", read(s.listShares))
	handle("POST /shares", write(s.shareProject))
	handle("POST /shares/accept", write(s.acceptInvite))
	handle("DELETE /shares/{id}", write(s.deleteShare))
	handle("GET /shares/config", read(s.getMailboxConfig))
	handle("PUT /shares/config", write(s.putMailboxConfig))

	// items
	handle("GET /items", read(s.listItems))
	handle("POST /items", write(s.createItem))
	handle("GET /items/{id}", read(s.getItem))
	handle("PATCH /items/{id}", write(s.patchItem))
	handle("DELETE /items/{id}", write(s.deleteItem))
	handle("GET /items/{id}/links", read(s.listLinks))
	handle("GET /items/{id}/refs", read(s.listItemRefs))
	handle("POST /items/{id}/refs", write(s.createItemRef))

	// attachments (screenshots) — blobs live in object storage (MinIO)
	handle("GET /items/{id}/attachments", read(s.listItemAttachments))
	handle("POST /items/{id}/attachments", write(s.uploadAttachmentsAuthed))
	handle("GET /attachments/{id}", read(s.getAttachment))
	handle("DELETE /attachments/{id}", write(s.deleteAttachment))

	// item links
	handle("POST /links", write(s.createLink))
	handle("DELETE /links/{id}", write(s.deleteLink))

	// item refs (code grounding)
	handle("DELETE /refs/{id}", write(s.deleteItemRef))

	// activity
	handle("GET /activity", read(s.listActivity))
	handle("POST /activity", write(s.createActivity))

	// context (orient)
	handle("GET /context", read(s.globalContext))
	handle("GET /context/{slug}", read(s.projectContext))
	handle("GET /agenda", read(s.agenda))

	// agent decision helpers
	handle("GET /next-action", read(s.nextAction))
	handle("GET /digest/{slug}", read(s.digest))
	handle("GET /stale", read(s.stale))

	// webhooks
	handle("GET /webhooks", read(s.listWebhooks))
	handle("GET /webhooks/events", read(s.listWebhookEvents))
	handle("POST /webhooks", write(s.createWebhook))
	handle("DELETE /webhooks/{id}", write(s.deleteWebhook))

	// live updates (SSE) — the web UI subscribes here instead of polling
	handle("GET /stream", read(s.stream))

	// search
	handle("GET /search", read(s.search))

	// usage analytics (how agents use the service)
	handle("GET /usage", read(s.usageReport))
	handle("GET /context-impact", read(s.listContextImpact))
	handle("POST /context-impact", write(s.createContextImpact))

	// ingest — public, cross-origin, and rate-limited (its key is embedded in
	// public HTML). CORS wraps the outside so the preflight skips auth.
	ingestLimiter := ratelimit.New(1, 10) // ~1 report/sec/IP, burst 10
	limited := func(h http.Handler) http.Handler {
		return ingestLimiter.Middleware(func(w http.ResponseWriter) { writeError(w, http.StatusTooManyRequests, "rate limit exceeded") }, h)
	}
	handle("POST /ingest/bug", corsIngest(limited(ingest(s.ingestBug))))
	handle("OPTIONS /ingest/bug", corsIngest(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))

	// screenshot upload for a just-filed report — same posture as /ingest/bug,
	// plus a source + freshness guard (see ingestUploadAttachments)
	handle("POST /ingest/attachments/{id}", corsIngest(limited(ingest(s.ingestUploadAttachments))))
	handle("OPTIONS /ingest/attachments/{id}", corsIngest(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))

	// quick capture (Apple Shortcuts / scripts) — same posture as /ingest/bug
	handle("POST /ingest/capture", corsIngest(limited(ingest(s.ingestCapture))))
	handle("OPTIONS /ingest/capture", corsIngest(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	handle("GET /ingest/projects", corsIngest(limited(ingest(s.ingestProjects))))

	// first-run setup — status is unauthenticated (the SPA must decide whether
	// to show the wizard before any key exists); completion needs the one-time
	// setup token. Post-setup edits go through key-authed /settings.
	handle("GET /setup/status", http.HandlerFunc(s.setupStatus))
	handle("POST /setup/complete", http.HandlerFunc(s.completeSetup))
	handle("GET /settings", read(s.getSettings))
	handle("PUT /settings", write(s.putSettings))

	return http.StripPrefix("/api", mux), patterns
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeDBError maps pgx.ErrNoRows to 404, a version conflict to 409, and
// everything else to a generic 500. The real error is logged server-side
// rather than leaked to the client.
func writeDBError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if errors.Is(err, service.ErrConflict) {
		writeError(w, http.StatusConflict, service.ErrConflict.Error())
		return
	}
	if errors.Is(err, service.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, service.ErrIdempotencyConflict.Error())
		return
	}
	log.Printf("db error: %v", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("empty request body")
		}
		return err
	}
	return nil
}
