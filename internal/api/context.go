package api

import (
	"net/http"

	"flightdeck/internal/service"
)

// restVerbosity defaults to full (the web UI renders full bodies); an explicit
// ?verbosity=compact opts into the truncated, token-economy shape.
func restVerbosity(r *http.Request) service.Verbosity {
	if r.URL.Query().Get("verbosity") == string(service.VerbosityCompact) {
		return service.VerbosityCompact
	}
	return service.VerbosityFull
}

func (s *Server) projectContext(w http.ResponseWriter, r *http.Request) {
	bundle, err := s.Svc.ProjectContext(r.Context(), r.PathValue("slug"), restVerbosity(r))
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bundle)
}

func (s *Server) globalContext(w http.ResponseWriter, r *http.Request) {
	bundle, err := s.Svc.GlobalContext(r.Context(), restVerbosity(r))
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bundle)
}
