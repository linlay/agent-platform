package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/kbasescenter"
	"errors"
	"net/http"
	"strings"
)

func (s *Server) handleKBasesCenter(w http.ResponseWriter, r *http.Request) {
	service := s.deps.KBasesCenter
	if service == nil {
		writeJSON(w, 503, api.Failure(503, "knowledge base center unavailable"))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/kbases")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var result any
	var err error
	decode := func(v any) bool {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if e := decodeJSON(r, v); e != nil {
			writeJSON(w, 400, api.Failure(400, "invalid request body"))
			return false
		}
		return true
	}
	method := func(allowed string) bool {
		if r.Method != allowed {
			w.Header().Set("Allow", allowed)
			writeJSON(w, 405, api.Failure(405, "method not allowed"))
			return false
		}
		return true
	}
	if path == "" {
		switch r.Method {
		case http.MethodGet:
			result, err = service.List()
		case http.MethodPost:
			var input kbasescenter.Input
			if !decode(&input) {
				return
			}
			result, err = service.Create(input)
		default:
			w.Header().Set("Allow", "GET, POST")
			writeJSON(w, 405, api.Failure(405, "method not allowed"))
			return
		}
	} else if len(parts) == 1 && parts[0] != "" {
		switch r.Method {
		case http.MethodGet:
			result, err = service.Get(parts[0])
		case http.MethodPut:
			var input kbasescenter.Input
			if !decode(&input) {
				return
			}
			result, err = service.Edit(parts[0], input)
		case http.MethodDelete:
			err = service.Delete(parts[0])
			result = map[string]bool{"deleted": err == nil}
		default:
			w.Header().Set("Allow", "GET, PUT, DELETE")
			writeJSON(w, 405, api.Failure(405, "method not allowed"))
			return
		}
	} else if len(parts) == 2 && parts[0] != "" {
		switch parts[1] {
		case "refresh":
			if !method(http.MethodPost) {
				return
			}
			result, err = service.Refresh(parts[0])
		case "search":
			if !method(http.MethodPost) {
				return
			}
			var input kbasescenter.SearchInput
			if !decode(&input) {
				return
			}
			result, err = service.Search(r.Context(), parts[0], input)
		case "status", "files", "read":
			if !method(http.MethodGet) {
				return
			}
			result, err = service.Read(r.Context(), parts[0], parts[1], r.URL.Query().Get("ref"), 0)
		default:
			writeJSON(w, 404, api.Failure(404, "not found"))
			return
		}
	} else {
		writeJSON(w, 404, api.Failure(404, "not found"))
		return
	}
	if err != nil {
		status := 400
		if errors.Is(err, kbasescenter.ErrNotFound) {
			status = 404
		}
		if errors.Is(err, kbasescenter.ErrBusy) {
			status = 409
		}
		writeJSON(w, status, api.Failure(status, err.Error()))
		return
	}
	writeJSON(w, 200, api.Success(result))
}
