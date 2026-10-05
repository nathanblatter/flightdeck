package api

import (
	"net/http"
)

// corsIngest answers the cross-origin preflight and sets permissive CORS headers
// for the public bug-ingest endpoint only. The embeddable widget POSTs JSON from
// other origins, which triggers an OPTIONS preflight that must NOT require auth.
func corsIngest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
