package api

import (
	"net/http"

	"flightdeck/internal/dto"
)

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := q.Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	projectID, err := optUUID(q, "project_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project_id")
		return
	}
	if slug := q.Get("project"); slug != "" && !projectID.Valid {
		p, err := s.St.GetProjectBySlug(r.Context(), slug)
		if err != nil {
			writeDBError(w, err)
			return
		}
		projectID = pgUUID(p.ID)
	}
	items, acts, err := s.Svc.SearchSmart(r.Context(), query, projectID, optStr(q, "type"), nil, 0, 0)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.SearchResults{
		Query:    query,
		Items:    items,
		Activity: acts,
	})
}
