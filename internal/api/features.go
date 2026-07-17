package api

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"flightdeck/internal/dto"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

// nextAction ranks ready (open + unblocked) items, optionally within one project.
func (s *Server) nextAction(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lim, err := optInt32(q, "limit")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return
	}
	var limit int32
	if lim != nil {
		limit = *lim
	}
	out, err := s.Svc.NextAction(r.Context(), q.Get("project"), limit)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// digest rolls up a project's recent activity. `since` defaults to 7 days ago.
func (s *Server) digest(w http.ResponseWriter, r *http.Request) {
	since, err := optTime(r.URL.Query(), "since")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid since (want RFC3339)")
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	if since != nil {
		cutoff = *since
	}
	out, err := s.Svc.Digest(r.Context(), r.PathValue("slug"), cutoff)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// stale surfaces idle in_progress items, untriaged bugs, and stale summaries.
func (s *Server) stale(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ipDays, err := optInt32(q, "in_progress_days")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid in_progress_days")
		return
	}
	bugDays, err := optInt32(q, "bug_days")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid bug_days")
		return
	}
	ip := int32(3)
	if ipDays != nil {
		ip = *ipDays
	}
	bug := int32(7)
	if bugDays != nil {
		bug = *bugDays
	}
	now := time.Now()
	out, err := s.Svc.Stale(r.Context(), now.AddDate(0, 0, -int(ip)), now.AddDate(0, 0, -int(bug)))
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- item links ---

func (s *Server) listLinks(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	links, err := s.Svc.ListLinks(r.Context(), id)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToItemLinks(links))
}

type linkReq struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	var req linkReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	from, err := uuid.Parse(req.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid from id")
		return
	}
	to, err := uuid.Parse(req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid to id")
		return
	}
	if req.Kind != "" {
		if err := service.ValidateLinkKind(&req.Kind); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	link, err := s.Svc.LinkItems(r.Context(), from, to, req.Kind)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto.ToItemLink(link))
}

func (s *Server) deleteLink(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.Svc.DeleteLink(r.Context(), id); err != nil {
		writeDBError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- item refs (code grounding) ---

func (s *Server) listItemRefs(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	refs, err := s.Svc.ListItemRefs(r.Context(), id)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToItemRefs(refs))
}

type itemRefReq struct {
	Kind  string `json:"kind"`
	Ref   string `json:"ref"`
	Label string `json:"label"`
}

func (s *Server) createItemRef(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req itemRefReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Ref == "" {
		writeError(w, http.StatusBadRequest, "ref is required")
		return
	}
	if req.Kind != "" {
		if err := service.ValidateRefKind(&req.Kind); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	ref, err := s.Svc.AddItemRef(r.Context(), id, req.Kind, req.Ref, req.Label)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto.ToItemRef(ref))
}

func (s *Server) deleteItemRef(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.Svc.DeleteItemRef(r.Context(), id); err != nil {
		writeDBError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- webhooks ---

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	hooks, err := s.St.ListWebhooks(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToWebhooks(hooks))
}

// listWebhookEvents surfaces erroring / dead-lettered outbox rows for operators.
func (s *Server) listWebhookEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.St.ListFailedWebhookEvents(r.Context(), 100)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToWebhookEvents(events))
}

type webhookReq struct {
	Project string   `json:"project"` // slug; empty = all projects
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	Secret  *string  `json:"secret"`
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request) {
	var req webhookReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	var projectID pgtype.UUID
	if req.Project != "" {
		p, err := s.St.GetProjectBySlug(r.Context(), req.Project)
		if err != nil {
			writeDBError(w, err)
			return
		}
		projectID = pgUUID(p.ID)
	}
	hook, err := s.St.CreateWebhook(r.Context(), store.CreateWebhookParams{
		Url:       req.URL,
		ProjectID: projectID,
		Secret:    req.Secret,
		Events:    req.Events,
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto.ToWebhook(hook))
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.St.DeleteWebhook(r.Context(), id); err != nil {
		writeDBError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
