package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"flightdeck/internal/auth"
	"flightdeck/internal/dto"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

type contextImpactReq struct {
	SessionID             string   `json:"session_id"`
	Project               string   `json:"project"`
	Item                  *string  `json:"item"`
	Effect                string   `json:"effect"`
	Mechanism             string   `json:"mechanism"`
	ContextRefs           []string `json:"context_refs"`
	Evidence              string   `json:"evidence"`
	EstimatedMinutesDelta *int32   `json:"estimated_minutes_delta"`
	IdempotencyKey        *string  `json:"idempotency_key"`
}

func (s *Server) createContextImpact(w http.ResponseWriter, r *http.Request) {
	var req contextImpactReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	event, created, err := s.Svc.RecordContextImpact(r.Context(), service.ContextImpactInput{
		SessionID: req.SessionID, Project: req.Project, Item: req.Item,
		Effect: req.Effect, Mechanism: req.Mechanism, ContextRefs: req.ContextRefs,
		Evidence: req.Evidence, EstimatedMinutesDelta: req.EstimatedMinutesDelta,
		IdempotencyKey: req.IdempotencyKey,
	}, auth.Actor(r.Context()))
	if err != nil {
		if errors.Is(err, service.ErrInvalidContextImpact) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeDBError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, dto.ToContextImpactEvent(event))
}

func (s *Server) listContextImpact(w http.ResponseWriter, r *http.Request) {
	days, err := boundedInt(r, "days", 7, 1, 90)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := boundedInt(r, "limit", 100, 1, 500)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	events, err := s.St.ListContextImpactEvents(r.Context(), store.ListContextImpactEventsParams{
		RecordedAt: time.Now().AddDate(0, 0, -days),
		Project:    optStr(r.URL.Query(), "project"),
		Lim:        int32(limit),
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToContextImpactEvents(events))
}

func boundedInt(r *http.Request, name string, fallback, min, max int) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, errors.New(name + " must be between " + strconv.Itoa(min) + " and " + strconv.Itoa(max))
	}
	return value, nil
}
