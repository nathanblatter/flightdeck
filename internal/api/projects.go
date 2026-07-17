package api

import (
	"net/http"

	"flightdeck/internal/dto"
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
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	Status       *string `json:"status"`
	Summary      *string `json:"summary"`
	Instructions *string `json:"instructions"`
	RepoURL      *string `json:"repo_url"`
	SiteURL      *string `json:"site_url"`
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
	p, err := s.St.CreateProject(r.Context(), store.CreateProjectParams{
		Slug:         req.Slug,
		Name:         req.Name,
		Status:       req.Status,
		Summary:      req.Summary,
		Instructions: req.Instructions,
		RepoUrl:      req.RepoURL,
		SiteUrl:      req.SiteURL,
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto.ToProject(p))
}

type patchProjectReq struct {
	Name         *string `json:"name"`
	Status       *string `json:"status"`
	Summary      *string `json:"summary"`
	Instructions *string `json:"instructions"`
	RepoURL      *string `json:"repo_url"`
	SiteURL      *string `json:"site_url"`
}

func (s *Server) patchProject(w http.ResponseWriter, r *http.Request) {
	var req patchProjectReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.St.UpdateProject(r.Context(), store.UpdateProjectParams{
		Slug:         r.PathValue("slug"),
		Name:         req.Name,
		Status:       req.Status,
		Summary:      req.Summary,
		Instructions: req.Instructions,
		RepoUrl:      req.RepoURL,
		SiteUrl:      req.SiteURL,
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto.ToProject(p))
}
