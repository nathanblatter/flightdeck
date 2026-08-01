package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"flightdeck/internal/auth"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

// First-run setup: a fresh instance (no keys, no settings) exposes a wizard in
// the SPA. The wizard authenticates with the one-time token that `flightdeck
// up` writes to the instance .env (or that serve logs on boot) — the API is
// otherwise key-authed and a fresh DB has no keys to auth with. "Open while
// empty" was rejected: even on a localhost bind it races the wizard against
// any local process or a DNS-rebinding page.

type setupStatusResp struct {
	SetupComplete bool   `json:"setup_complete"`
	InstanceName  string `json:"instance_name"`
	Version       string `json:"version"`
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	done, err := s.isSetupComplete(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, setupStatusResp{
		SetupComplete: done,
		InstanceName:  s.Svc.InstanceName(),
		Version:       s.Version,
	})
}

// isSetupComplete caches the positive answer: once set up, always set up.
func (s *Server) isSetupComplete(ctx context.Context) (bool, error) {
	if s.setupDone.Load() {
		return true, nil
	}
	done, err := s.Svc.SetupComplete(ctx)
	if err != nil {
		return false, err
	}
	if done {
		s.setupDone.Store(true)
	}
	return done, nil
}

type setupKeyReq struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

type setupCompleteReq struct {
	InstanceName string          `json:"instance_name"`
	OpenAIAPIKey string          `json:"openai_api_key"`
	Flags        map[string]bool `json:"flags"`
	Keys         []setupKeyReq   `json:"keys"`
}

type setupKeyResp struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	Key    string   `json:"key"` // raw secret, shown once
}

func validScope(s string) bool {
	return s == auth.ScopeRead || s == auth.ScopeWrite || s == auth.ScopeIngest
}

func (s *Server) completeSetup(w http.ResponseWriter, r *http.Request) {
	// Serialize completion attempts so two valid-token requests can't both mint.
	s.setupMu.Lock()
	defer s.setupMu.Unlock()

	done, err := s.isSetupComplete(r.Context())
	if err != nil {
		writeDBError(w, err)
		return
	}
	if done {
		writeError(w, http.StatusGone, "setup already completed")
		return
	}
	if s.SetupToken == "" {
		writeError(w, http.StatusServiceUnavailable, "no setup token configured")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Setup-Token")), []byte(s.SetupToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid setup token")
		return
	}

	var req setupCompleteReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.InstanceName = strings.TrimSpace(req.InstanceName)
	if req.InstanceName == "" {
		writeError(w, http.StatusBadRequest, "instance_name is required")
		return
	}
	if len(req.Keys) == 0 {
		writeError(w, http.StatusBadRequest, "at least one key is required")
		return
	}
	for _, k := range req.Keys {
		if strings.TrimSpace(k.Name) == "" || len(k.Scopes) == 0 {
			writeError(w, http.StatusBadRequest, "each key needs a name and at least one scope")
			return
		}
		for _, sc := range k.Scopes {
			if !validScope(sc) {
				writeError(w, http.StatusBadRequest, "invalid scope: "+sc)
				return
			}
		}
	}

	ctx := r.Context()
	if err := s.Svc.PutSetting(ctx, service.SettingInstanceName, req.InstanceName); err != nil {
		writeDBError(w, err)
		return
	}
	if req.OpenAIAPIKey != "" {
		if err := s.Svc.PutSetting(ctx, service.SettingOpenAIKey, req.OpenAIAPIKey); err != nil {
			writeDBError(w, err)
			return
		}
	}
	if req.Flags != nil {
		if err := s.Svc.PutSetting(ctx, service.SettingFlags, req.Flags); err != nil {
			writeDBError(w, err)
			return
		}
	}

	out := make([]setupKeyResp, 0, len(req.Keys))
	for _, k := range req.Keys {
		raw, err := auth.NewRawKey("fd_")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "key generation failed")
			return
		}
		if _, err := s.St.CreateAPIKey(ctx, store.CreateAPIKeyParams{
			Name:    strings.TrimSpace(k.Name),
			KeyHash: auth.HashKey(raw),
			Scopes:  k.Scopes,
		}); err != nil {
			writeDBError(w, err)
			return
		}
		out = append(out, setupKeyResp{Name: k.Name, Scopes: k.Scopes, Key: raw})
	}

	if err := s.Svc.PutSetting(ctx, service.SettingSetupComplete, true); err != nil {
		writeDBError(w, err)
		return
	}
	s.setupDone.Store(true)
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

// --- post-setup settings (key-authed) ---

type settingsResp struct {
	InstanceName     string          `json:"instance_name"`
	Flags            map[string]bool `json:"flags"`
	OpenAIConfigured bool            `json:"openai_configured"`
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, settingsResp{
		InstanceName:     s.Svc.InstanceName(),
		Flags:            s.Svc.FlagsSnapshot(),
		OpenAIConfigured: s.Svc.EmbeddingEnabled(),
	})
}

type settingsPatchReq struct {
	InstanceName *string         `json:"instance_name"`
	OpenAIAPIKey *string         `json:"openai_api_key"`
	Flags        map[string]bool `json:"flags"`
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsPatchReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	if req.InstanceName != nil {
		name := strings.TrimSpace(*req.InstanceName)
		if name == "" {
			writeError(w, http.StatusBadRequest, "instance_name cannot be empty")
			return
		}
		if err := s.Svc.PutSetting(ctx, service.SettingInstanceName, name); err != nil {
			writeDBError(w, err)
			return
		}
	}
	if req.OpenAIAPIKey != nil {
		if err := s.Svc.PutSetting(ctx, service.SettingOpenAIKey, *req.OpenAIAPIKey); err != nil {
			writeDBError(w, err)
			return
		}
	}
	if req.Flags != nil {
		if err := s.Svc.PutSetting(ctx, service.SettingFlags, req.Flags); err != nil {
			writeDBError(w, err)
			return
		}
	}
	s.getSettings(w, r)
}
