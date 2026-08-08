package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	projectpkg "agent-platform/internal/project"
)

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

func (s *Server) projectService() projectpkg.Service {
	reader, _ := s.deps.Tools.(contracts.ProjectFileHistoryReader)
	return projectpkg.Service{
		Registry:     s.deps.Registry,
		Chats:        s.deps.Chats,
		History:      reader,
		ChatsRoot:    s.deps.Config.Paths.ChatsDir,
		MaxReadBytes: s.deps.Config.FileTools.MaxReadBytes,
	}
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
