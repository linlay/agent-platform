package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"agent-platform/internal/api"
	projectpkg "agent-platform/internal/project"
)

func (s *Server) handleProjectGit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	response, err := s.projectService().Git(r.Context(), r.URL.Query().Get("agentKey"))
	s.writeProjectHTTPResponse(w, response, err)
}

func (s *Server) handleProjectGitBranches(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		response, err := s.projectService().GitBranches(r.Context(), r.URL.Query().Get("agentKey"))
		s.writeProjectHTTPResponse(w, response, err)
	case http.MethodPost:
		var request api.ProjectGitBranchRequest
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		if err := decodeJSON(r, &request); err != nil {
			s.writeProjectHTTPResponse(w, nil, projectpkg.Error{Status: 400, Code: "invalid_request", Message: "invalid branch request"})
			return
		}
		response, err := s.projectService().ChangeGitBranch(r.Context(), request)
		s.writeProjectHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", "GET, POST")
		s.writeProjectHTTPResponse(w, nil, projectpkg.Error{Status: 405, Code: "method_not_allowed", Message: "method not allowed"})
	}
}

func (s *Server) handleProjectTree(w http.ResponseWriter, r *http.Request) {
	limit, err := projectPageLimit(r.URL.Query().Get("limit"))
	if err != nil {
		s.writeProjectHTTPResponse(w, nil, err)
		return
	}
	response, err := s.projectService().Tree(
		r.URL.Query().Get("agentKey"),
		r.URL.Query().Get("path"),
		r.URL.Query().Get("cursor"),
		limit,
	)
	s.writeProjectHTTPResponse(w, response, err)
}

func (s *Server) handleProjectChanges(w http.ResponseWriter, r *http.Request) {
	limit, err := projectPageLimit(r.URL.Query().Get("limit"))
	if err != nil {
		s.writeProjectHTTPResponse(w, nil, err)
		return
	}
	response, err := s.projectService().Changes(
		r.URL.Query().Get("agentKey"),
		r.URL.Query().Get("chatId"),
		r.URL.Query().Get("runId"),
		r.URL.Query().Get("cursor"),
		limit,
	)
	s.writeProjectHTTPResponse(w, response, err)
}

func (s *Server) handleProjectDiff(w http.ResponseWriter, r *http.Request) {
	response, err := s.projectService().Diff(
		r.URL.Query().Get("agentKey"),
		r.URL.Query().Get("chatId"),
		r.URL.Query().Get("runId"),
		r.URL.Query().Get("path"),
		r.URL.Query().Get("encoding"),
	)
	s.writeProjectHTTPResponse(w, response, err)
}

func (s *Server) projectService() *projectpkg.Service {
	if s.project == nil {
		return nil
	}
	service := *s.project
	// Config is immutable in production, while tests may replace this limit on
	// an assembled Server. Preserve that established behavior without rebuilding
	// the service dependencies in the transport layer.
	service.MaxReadBytes = s.deps.Config.FileTools.MaxReadBytes
	return &service
}

func (s *Server) writeProjectHTTPResponse(w http.ResponseWriter, response any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, api.Success(response))
		return
	}
	var projectErr projectpkg.Error
	if errors.As(err, &projectErr) {
		writeJSON(w, projectErr.Status, api.Failure(projectErr.Status, projectErr.Message, map[string]any{
			"code": projectErr.Code,
		}))
		return
	}
	writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
}

func projectPageLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return projectpkg.DefaultPageLimit, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > projectpkg.MaxPageLimit {
		return 0, projectpkg.Error{
			Status:  http.StatusBadRequest,
			Code:    "invalid_request",
			Message: "limit must be an integer between 1 and 1000",
		}
	}
	return value, nil
}
