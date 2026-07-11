package server

import (
	"net/http"
	"strings"

	"agent-platform/internal/agent/kbase"
	"agent-platform/internal/api"
)

type kbaseRefreshRequest struct {
	Force bool `json:"force,omitempty"`
}

func (s *Server) handleKBase(w http.ResponseWriter, r *http.Request) {
	agentKey, action, ok := parseKBasePath(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "not found"))
		return
	}
	if s.deps.KBase == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(http.StatusServiceUnavailable, "kbase is not configured"))
		return
	}
	if err := s.deps.KBase.ValidateAgent(agentKey); err != nil {
		statusCode := kbaseErrorStatus(err)
		writeJSON(w, statusCode, api.Failure(statusCode, kbaseErrorMessage(err)))
		return
	}
	switch action {
	case "status":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
			return
		}
		status, err := s.deps.KBase.Status(agentKey)
		if err != nil {
			statusCode := kbaseErrorStatus(err)
			writeJSON(w, statusCode, api.Failure(statusCode, kbaseErrorMessage(err)))
			return
		}
		writeJSON(w, http.StatusOK, api.Success(status))
	case "refresh":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
			return
		}
		var req kbaseRefreshRequest
		if r.Body != nil && r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid request body"))
				return
			}
		}
		result, err := s.deps.KBase.Refresh(r.Context(), agentKey, kbase.RefreshOptions{Force: req.Force, Mode: "manual"})
		if err != nil {
			statusCode := kbaseErrorStatus(err)
			writeJSON(w, statusCode, api.Failure(statusCode, kbaseErrorMessage(err)))
			return
		}
		writeJSON(w, http.StatusOK, api.Success(result))
	default:
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "not found"))
	}
}

func kbaseErrorStatus(err error) int {
	switch kbase.KindOf(err) {
	case kbase.ErrorUnavailable:
		return http.StatusServiceUnavailable
	case kbase.ErrorNotFound:
		return http.StatusNotFound
	case kbase.ErrorWrongMode:
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}

func kbaseErrorMessage(err error) string {
	switch kbase.KindOf(err) {
	case kbase.ErrorNotFound:
		return "agent not found"
	case kbase.ErrorWrongMode:
		return "agent is not mode: KBASE"
	default:
		return err.Error()
	}
}

func parseKBasePath(path string) (string, string, bool) {
	rest := strings.TrimPrefix(path, "/api/kbase/")
	if rest == path || rest == "" {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 {
		return "", "", false
	}
	agentKey := strings.TrimSpace(parts[0])
	action := strings.TrimSpace(parts[1])
	if agentKey == "" || action == "" {
		return "", "", false
	}
	return agentKey, action, true
}
