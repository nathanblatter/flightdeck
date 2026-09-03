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
	mux := http.NewServeMux()

	read := func(h http.HandlerFunc) http.Handler { return auth.Middleware(s.St, auth.ScopeRead)(h) }
	write := func(h http.HandlerFunc) http.Handler { return auth.Middleware(s.St, auth.ScopeWrite)(h) }
	ingest := func(h http.HandlerFunc) http.Handler { return auth.Middleware(s.St, auth.ScopeIngest)(h) }

	// projects
	mux.Handle("GET /projects", read(s.listProjects))
	mux.Handle("POST /projects", write(s.createProject))
	mux.Handle("GET /projects/{slug}", read(s.getProject))
	mux.Handle("PATCH /projects/{slug}", write(s.patchProject))

	// items
	mux.Handle("GET /items", read(s.listItems))
	mux.Handle("POST /items", write(s.createItem))
	mux.Handle("GET /items/{id}", read(s.getItem))
	mux.Handle("PATCH /items/{id}", write(s.patchItem))
	mux.Handle("DELETE /items/{id}", write(s.deleteItem))
	mux.Handle("GET /items/{id}/links", read(s.listLinks))
	mux.Handle("GET /items/{id}/refs", read(s.listItemRefs))
	mux.Handle("POST /items/{id}/refs", write(s.createItemRef))

	// attachments (screenshots) — blobs live in object storage (MinIO)
	mux.Handle("GET /items/{id}/attachments", read(s.listItemAttachments))
	mux.Handle("POST /items/{id}/attachments", write(s.uploadAttachmentsAuthed))
	mux.Handle("GET /attachments/{id}", read(s.getAttachment))
	mux.Handle("DELETE /attachments/{id}", write(s.deleteAttachment))

	// item links
	mux.Handle("POST /links", write(s.createLink))
	mux.Handle("DELETE /links/{id}", write(s.deleteLink))

	// item refs (code grounding)
	mux.Handle("DELETE /refs/{id}", write(s.deleteItemRef))

	// activity
	mux.Handle("GET /activity", read(s.listActivity))
	mux.Handle("POST /activity", write(s.createActivity))

	// context (orient)
	mux.Handle("GET /context", read(s.globalContext))
	mux.Handle("GET /context/{slug}", read(s.projectContext))

	// agent decision helpers
	mux.Handle("GET /next-action", read(s.nextAction))
	mux.Handle("GET /digest/{slug}", read(s.digest))
	mux.Handle("GET /stale", read(s.stale))

	// webhooks
	mux.Handle("GET /webhooks", read(s.listWebhooks))
	mux.Handle("GET /webhooks/events", read(s.listWebhookEvents))
	mux.Handle("POST /webhooks", write(s.createWebhook))
	mux.Handle("DELETE /webhooks/{id}", write(s.deleteWebhook))

	// live updates (SSE) — the web UI subscribes here instead of polling
	mux.Handle("GET /stream", read(s.stream))

	// search
	mux.Handle("GET /search", read(s.search))

	// usage analytics (how agents use the service)
	mux.Handle("GET /usage", read(s.usageReport))
	mux.Handle("GET /context-impact", read(s.listContextImpact))
	mux.Handle("POST /context-impact", write(s.createContextImpact))

	// ingest — public, cross-origin, and rate-limited (its key is embedded in
	// public HTML). CORS wraps the outside so the preflight skips auth.
	ingestLimiter := newIPLimiter(1, 10) // ~1 report/sec/IP, burst 10
	mux.Handle("POST /ingest/bug", corsIngest(ingestLimiter.middleware(ingest(s.ingestBug))))
	mux.Handle("OPTIONS /ingest/bug", corsIngest(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))

	// screenshot upload for a just-filed report — same posture as /ingest/bug,
	// plus a source + freshness guard (see ingestUploadAttachments)
	mux.Handle("POST /ingest/attachments/{id}", corsIngest(ingestLimiter.middleware(ingest(s.ingestUploadAttachments))))
	mux.Handle("OPTIONS /ingest/attachments/{id}", corsIngest(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))

	// quick capture (Apple Shortcuts / scripts) — same posture as /ingest/bug
	mux.Handle("POST /ingest/capture", corsIngest(ingestLimiter.middleware(ingest(s.ingestCapture))))
	mux.Handle("OPTIONS /ingest/capture", corsIngest(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	mux.Handle("GET /ingest/projects", corsIngest(ingestLimiter.middleware(ingest(s.ingestProjects))))

	// first-run setup — status is unauthenticated (the SPA must decide whether
	// to show the wizard before any key exists); completion needs the one-time
	// setup token. Post-setup edits go through key-authed /settings.
	mux.Handle("GET /setup/status", http.HandlerFunc(s.setupStatus))
	mux.Handle("POST /setup/complete", http.HandlerFunc(s.completeSetup))
	mux.Handle("GET /settings", read(s.getSettings))
	mux.Handle("PUT /settings", write(s.putSettings))

	return http.StripPrefix("/api", mux)
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
