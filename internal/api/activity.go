package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"flightdeck/internal/auth"
	"flightdeck/internal/dto"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

func (s *Server) listActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
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
	itemID, err := optUUID(q, "item_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid item_id")
		return
	}
	since, err := optTime(q, "since")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid since (want RFC3339)")
		return
	}
	lim, err := optInt32(q, "limit")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return
	}
	rows, err := s.St.ListActivity(r.Context(), store.ListActivityParams{
		ProjectID: projectID,
		ItemID:    itemID,
		Kind:      optStr(q, "kind"),
		Since:     since,
		Lim:       lim,
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToActivities(rows))
}

type createActivityReq struct {
	Project    string          `json:"project"` // slug
	ProjectID  *string         `json:"project_id"`
	ItemID     *string         `json:"item_id"`
	Kind       *string         `json:"kind"`
	Body       *string         `json:"body"`
	Confidence *string         `json:"confidence"`
	Metadata   json.RawMessage `json:"metadata"`
}

func (s *Server) createActivity(w http.ResponseWriter, r *http.Request) {
	var req createActivityReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := service.ValidateActivityKind(req.Kind); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := service.ValidateConfidence(req.Confidence); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	projectID, err := s.resolveProjectID(r, req.Project, req.ProjectID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if projectID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "project (slug) or project_id is required")
		return
	}
	itemUUID, err := pgUUIDFromPtr(req.ItemID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid item_id")
		return
	}
	actor := auth.Actor(r.Context())
	row, err := s.Svc.LogActivity(r.Context(), store.CreateActivityParams{
		ProjectID:  projectID,
		ItemID:     itemUUID,
		Kind:       req.Kind,
		Actor:      &actor,
		Body:       req.Body,
		Confidence: req.Confidence,
		Metadata:   req.Metadata,
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto.ToActivity(row))
}
