package metrics

import (
	"net/http"
	"regexp"
	"strings"
	"time"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// normalizeRoute collapses high-cardinality path segments (UUIDs) to keep the
// metric label space bounded — so /api/items/<uuid> becomes /api/items/:id.
func normalizeRoute(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if uuidRe.MatchString(p) {
			parts[i] = ":id"
		}
	}
	return strings.Join(parts, "/")
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush propagates to the underlying writer when it supports flushing (SSE/MCP).
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// HTTPMiddleware records RED metrics for every served request.
func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		HTTP(r.Method, normalizeRoute(r.URL.Path), sw.status, time.Since(start))
	})
}
