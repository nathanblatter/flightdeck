package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"flightdeck/internal/auth"
	"flightdeck/internal/dto"
	"flightdeck/internal/store"
)

// maxBugMessageLen caps an ingested bug message; the request body is already
// LimitReader-capped at 1MB in decodeJSON, this bounds the stored title/body.
const maxBugMessageLen = 5000

// bugReport is the minimal payload the embeddable widget POSTs. `site` is the
// project slug the widget was configured with.
type bugReport struct {
	Site     string          `json:"site"`
	URL      string          `json:"url"`
	Message  string          `json:"message"`
	Severity string          `json:"severity"`
	Meta     json.RawMessage `json:"meta"`
}

// severityToPriority maps a reporter's severity to an item priority.
func severityToPriority(sev string) string {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "low":
		return "low"
	case "high":
		return "high"
	case "urgent", "critical":
		return "urgent"
	default:
		return "med"
	}
}

func (s *Server) ingestBug(w http.ResponseWriter, r *http.Request) {
	var rep bugReport
	if err := decodeJSON(r, &rep); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if rep.Site == "" || rep.Message == "" {
		writeError(w, http.StatusBadRequest, "site and message are required")
		return
	}
	if len(rep.Message) > maxBugMessageLen {
		writeError(w, http.StatusBadRequest, "message too long")
		return
	}
	project, err := s.St.GetProjectBySlug(r.Context(), rep.Site)
	if err != nil {
		writeDBError(w, err)
		return
	}

	title := strings.TrimSpace(rep.Message)
	if len(title) > 80 {
		title = title[:77] + "..."
	}

	metadata := buildBugMetadata(rep)
	var extRef *string
	if rep.URL != "" {
		extRef = &rep.URL
	}
	priority := severityToPriority(rep.Severity)
	typ, source := "bug", "bug_reporter"

	item, err := s.Svc.CreateItem(r.Context(), store.CreateItemParams{
		ProjectID:   project.ID,
		Type:        &typ,
		Title:       title,
		Body:        &rep.Message,
		Priority:    &priority,
		Source:      &source,
		ExternalRef: extRef,
		Tags:        []string{"bug-report"},
		Metadata:    metadata,
	}, auth.Actor(r.Context()))
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto.ToItem(item))
}

// buildBugMetadata merges the reporter-supplied meta with the source url/severity.
func buildBugMetadata(rep bugReport) json.RawMessage {
	m := map[string]any{}
	if len(rep.Meta) > 0 {
		_ = json.Unmarshal(rep.Meta, &m)
	}
	if rep.URL != "" {
		m["url"] = rep.URL
	}
	if rep.Severity != "" {
		m["severity"] = rep.Severity
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}
