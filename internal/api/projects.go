package api

import (
	"errors"
	"net/http"
	"strings"

	"flightdeck/internal/dto"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	status := optStr(r.URL.Query(), "status")
	projects, err := s.St.ListProjects(r.Context(), status)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToProjects(projects))
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.St.GetProjectBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToProject(p))
}

type createProjectReq struct {
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Status       *string  `json:"status"`
	Summary      *string  `json:"summary"`
	Instructions *string  `json:"instructions"`
	Aliases      []string `json:"aliases"`
	RepoURL      *string  `json:"repo_url"`
	SiteURL      *string  `json:"site_url"`
	// Parent is the slug of an existing project to nest under; empty/omitted
	// creates a root project.
	Parent *string `json:"parent"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Slug == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "slug and name are required")
		return
	}
	if err := service.ValidateProjectStatus(req.Status); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	parent, err := checkParent(s, r, req.Slug, req.Parent)
	if err != nil {
		writeParentError(w, err)
		return
	}
	p, err := s.St.CreateProject(r.Context(), store.CreateProjectParams{
		Slug:         req.Slug,
		Name:         req.Name,
		Status:       req.Status,
		Summary:      req.Summary,
		Instructions: req.Instructions,
		Aliases:      req.Aliases,
		RepoUrl:      req.RepoURL,
		SiteUrl:      req.SiteURL,
		ParentSlug:   parent,
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto.ToProject(p))
}

type patchProjectReq struct {
	Name         *string  `json:"name"`
	Status       *string  `json:"status"`
	Summary      *string  `json:"summary"`
	Instructions *string  `json:"instructions"`
	Aliases      []string `json:"aliases"`
	RepoURL      *string  `json:"repo_url"`
	SiteURL      *string  `json:"site_url"`
	// Parent is tri-state: omitted leaves the parent unchanged, "" clears it
	// (project becomes a root), a slug re-parents under that project.
	Parent *string `json:"parent"`
}

func (s *Server) patchProject(w http.ResponseWriter, r *http.Request) {
	var req patchProjectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := service.ValidateProjectStatus(req.Status); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	slug := r.PathValue("slug")
	parent, err := checkParent(s, r, slug, req.Parent)
	if err != nil {
		writeParentError(w, err)
		return
	}
	p, err := s.St.UpdateProject(r.Context(), store.UpdateProjectParams{
		Slug:         slug,
		Name:         req.Name,
		Status:       req.Status,
		Summary:      req.Summary,
		Instructions: req.Instructions,
		Aliases:      req.Aliases,
		RepoUrl:      req.RepoURL,
		SiteUrl:      req.SiteURL,
		SetParent:    req.Parent != nil,
		ParentSlug:   parent,
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToProject(p))
}

// deleteProject purges an archived project and everything under it. This is
// irreversible, so it is gated twice: the project must already be archived
// (PATCH status=archived), and ?confirm= must repeat the slug — a bare DELETE
// on the right URL is not enough to destroy a project's history.
func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if r.URL.Query().Get("confirm") != slug {
		writeError(w, http.StatusBadRequest, "confirm query parameter must equal the project slug")
		return
	}
	counts, err := s.Svc.PurgeProject(r.Context(), slug)
	if err != nil {
		if errors.Is(err, service.ErrProjectNotEmpty) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

// checkParent validates a requested parent change and normalizes it to the
// store's nullable form (nil = root). A nil req means "leave unchanged".
func checkParent(s *Server, r *http.Request, slug string, req *string) (*string, error) {
	if req == nil {
		return nil, nil
	}
	if err := s.Svc.ValidateProjectParent(r.Context(), slug, *req); err != nil {
		return nil, err
	}
	if trimmed := strings.TrimSpace(*req); trimmed != "" {
		return &trimmed, nil
	}
	return nil, nil
}

func writeParentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidProjectParent):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, "parent project not found")
	default:
		writeDBError(w, err)
	}
}
