package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"flightdeck/internal/service"
)

// shareView is the API shape of a share. It deliberately omits the tokens, the
// symmetric key, and the client private key: those are credentials, and the UI
// never needs them to show a share's status.
type shareView struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	PeerName   string     `json:"peer_name"`
	MailboxURL string     `json:"mailbox_url"`
	Enabled    bool       `json:"enabled"`
	LastSendAt *time.Time `json:"last_send_at,omitempty"`
	LastRecvAt *time.Time `json:"last_recv_at,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	rows, err := s.St.ListProjectShares(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	out := make([]shareView, 0, len(rows))
	for _, sh := range rows {
		out = append(out, shareView{
			ID: sh.ID.String(), ProjectID: sh.ProjectID.String(), PeerName: sh.PeerName,
			MailboxURL: sh.MailboxUrl, Enabled: sh.Enabled,
			LastSendAt: sh.LastSendAt, LastRecvAt: sh.LastRecvAt,
			LastError: sh.LastError, CreatedAt: sh.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type shareProjectReq struct {
	Project string `json:"project"`
	// PeerName labels the instance being invited, so a share list reads
	// "work-laptop" rather than a UUID.
	PeerName string `json:"peer_name"`
}

// shareProject creates a bidirectional share and returns the invite code.
//
// The code carries the encryption key, so it is returned exactly once here and
// never stored in retrievable form — losing it means revoking and re-sharing.
func (s *Server) shareProject(w http.ResponseWriter, r *http.Request) {
	var req shareProjectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Project == "" {
		writeError(w, http.StatusBadRequest, "project is required")
		return
	}
	code, err := s.Svc.ShareProject(r.Context(), req.Project, req.PeerName)
	if err != nil {
		if errors.Is(err, service.ErrSharingNotConfigured) {
			writeError(w, http.StatusPreconditionFailed, err.Error())
			return
		}
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{
		"invite": code,
		"note":   "This code contains the encryption key for the shared project. Send it over a channel you trust, and treat it like a password — it is shown only once.",
	})
}

type acceptInviteReq struct {
	Invite string `json:"invite"`
	// Slug optionally renames the project locally. Slugs are globally unique
	// per instance, so this is the escape hatch when the name is already taken.
	Slug string `json:"slug"`
}

func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var req acceptInviteReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Invite == "" {
		writeError(w, http.StatusBadRequest, "invite is required")
		return
	}
	projectID, err := s.Svc.AcceptInvite(r.Context(), req.Invite, req.Slug)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"project_id": projectID.String()})
}

func (s *Server) deleteShare(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid share id")
		return
	}
	if err := s.St.DeleteProjectShare(r.Context(), id); err != nil {
		writeDBError(w, err)
		return
	}
	// The local project and its items stay: unsharing stops the exchange, it
	// does not withdraw work that already arrived.
	w.WriteHeader(http.StatusNoContent)
}

// getMailboxConfig reports whether sharing is set up, without ever returning
// the admin token or client private key it holds.
func (s *Server) getMailboxConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Svc.MailboxConfig(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url":             cfg.URL,
		"configured":      cfg.URL != "" && cfg.ClientCert != "",
		"can_create":      cfg.AdminToken != "",
		"has_client_cert": cfg.ClientCert != "",
	})
}

func (s *Server) putMailboxConfig(w http.ResponseWriter, r *http.Request) {
	var cfg service.MailboxConfig
	if err := decodeJSON(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Svc.SetMailboxConfig(r.Context(), cfg); err != nil {
		writeDBError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
