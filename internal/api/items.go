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

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	projectID, err := optUUID(q, "project_id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid project_id")
		return
	}
	// Allow filtering by project slug as a convenience.
	if slug := q.Get("project"); slug != "" && !projectID.Valid {
		p, err := s.St.GetProjectBySlug(r.Context(), slug)
		if err != nil {
			writeDBError(w, err)
			return
		}
		projectID = pgUUID(p.ID)
	}
	since, err := optTime(q, "updated_since")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid updated_since (want RFC3339)")
		return
	}
	lim, err := optInt32(q, "limit")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return
	}
	items, err := s.St.ListItems(r.Context(), store.ListItemsParams{
		ProjectID:    projectID,
		Status:       optStr(q, "status"),
		Type:         optStr(q, "type"),
		Assignee:     optStr(q, "assignee"),
		Tag:          optStr(q, "tag"),
		UpdatedSince: since,
		Q:            optStr(q, "q"),
		Lim:          lim,
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	out := dto.ToItems(items)
	// Annotate with blocked flags (open "blocks" edges), same shape as the
	// project-context endpoint, so the kanban can badge blocked cards.
	edges, err := s.St.ListOpenBlockingEdges(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	blockedBy := make(map[string][]string, len(edges))
	for _, e := range edges {
		blockedBy[e.BlockedID.String()] = append(blockedBy[e.BlockedID.String()], e.BlockerTitle)
	}
	for i := range out {
		if titles, ok := blockedBy[out[i].ID]; ok {
			out[i].Blocked = true
			out[i].BlockedBy = titles
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	// Accept a UUID or a short ref (e.g. flightdeck-42), matching the MCP tools.
	raw := r.PathValue("id")
	var item store.Item
	var err error
	if id, perr := uuid.Parse(raw); perr == nil {
		item, err = s.St.GetItem(r.Context(), id)
	} else {
		item, err = s.St.GetItemByRef(r.Context(), raw)
	}
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToItem(item))
}

type createItemReq struct {
	Project            string          `json:"project"` // slug
	ProjectID          *string         `json:"project_id"`
	Type               *string         `json:"type"`
	Title              string          `json:"title"`
	Body               *string         `json:"body"`
	Status             *string         `json:"status"`
	Priority           *string         `json:"priority"`
	Assignee           *string         `json:"assignee"`
	Position           *float64        `json:"position"`
	Source             *string         `json:"source"`
	ExternalRef        *string         `json:"external_ref"`
	Tags               []string        `json:"tags"`
	Metadata           json.RawMessage `json:"metadata"`
	IdempotencyKey     *string         `json:"idempotency_key"`
	AcceptanceCriteria json.RawMessage `json:"acceptance_criteria"`
}

func (s *Server) createItem(w http.ResponseWriter, r *http.Request) {
	var req createItemReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if err := service.ValidateItemFields(req.Type, req.Status, req.Priority); err != nil {
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
	item, err := s.Svc.CreateItem(r.Context(), store.CreateItemParams{
		ProjectID:          projectID,
		Type:               req.Type,
		Title:              req.Title,
		Body:               req.Body,
		Status:             req.Status,
		Priority:           req.Priority,
		Assignee:           req.Assignee,
		Position:           req.Position,
		Source:             req.Source,
		ExternalRef:        req.ExternalRef,
		Tags:               req.Tags,
		Metadata:           req.Metadata,
		IdempotencyKey:     req.IdempotencyKey,
		AcceptanceCriteria: req.AcceptanceCriteria,
	}, auth.Actor(r.Context()))
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto.ToItem(item))
}

type patchItemReq struct {
	Title              *string         `json:"title"`
	Body               *string         `json:"body"`
	Type               *string         `json:"type"`
	Status             *string         `json:"status"`
	Priority           *string         `json:"priority"`
	Assignee           *string         `json:"assignee"`
	Position           *float64        `json:"position"`
	Tags               []string        `json:"tags"`
	Metadata           json.RawMessage `json:"metadata"`
	AcceptanceCriteria json.RawMessage `json:"acceptance_criteria"`
	ExpectedVersion    *int32          `json:"expected_version"`
}

func (s *Server) patchItem(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req patchItemReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := service.ValidateItemFields(req.Type, req.Status, req.Priority); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	item, err := s.Svc.UpdateItem(r.Context(), store.UpdateItemParams{
		ID:                 id,
		Title:              req.Title,
		Body:               req.Body,
		Type:               req.Type,
		Status:             req.Status,
		Priority:           req.Priority,
		Assignee:           req.Assignee,
		Position:           req.Position,
		Tags:               req.Tags,
		Metadata:           req.Metadata,
		AcceptanceCriteria: req.AcceptanceCriteria,
		ExpectedVersion:    req.ExpectedVersion,
	}, auth.Actor(r.Context()))
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToItem(item))
}

func (s *Server) deleteItem(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if _, err := s.St.SoftDeleteItem(r.Context(), id); err != nil {
		writeDBError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolveProjectID turns an explicit project_id or a project slug into a UUID.
func (s *Server) resolveProjectID(r *http.Request, slug string, idStr *string) (uuid.UUID, error) {
	if idStr != nil && *idStr != "" {
		return uuid.Parse(*idStr)
	}
	if slug != "" {
		p, err := s.St.GetProjectBySlug(r.Context(), slug)
		if err != nil {
			return uuid.Nil, err
		}
		return p.ID, nil
	}
	return uuid.Nil, nil
}
