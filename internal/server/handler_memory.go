package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"agent-platform/internal/api"
	"agent-platform/internal/memory"
)

func (s *Server) memoryResponse(w http.ResponseWriter, value any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, api.Success(value))
		return
	}
	status := http.StatusInternalServerError
	if errors.Is(err, memory.ErrInvalid) {
		status = http.StatusBadRequest
	}
	if errors.Is(err, memory.ErrConflict) {
		status = http.StatusConflict
	}
	message := err.Error()
	if status == http.StatusInternalServerError {
		message = "unable to access memory document"
	}
	writeJSON(w, status, api.Failure(status, message))
}

func (s *Server) handleMemoryFile(w http.ResponseWriter, r *http.Request) {
	if s.deps.Memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, api.Failure(503, "memory files are unavailable"))
		return
	}
	if r.Method == http.MethodGet {
		d, err := s.deps.Memory.Read(r.URL.Query().Get("kind"), r.URL.Query().Get("date"))
		s.memoryResponse(w, d, err)
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, PUT, DELETE")
		writeJSON(w, 405, api.Failure(405, "method not allowed"))
		return
	}
	var req struct {
		Kind     string  `json:"kind"`
		Date     string  `json:"date"`
		Content  *string `json:"content"`
		Revision string  `json:"revision"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2*memory.MaxFileBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		s.memoryResponse(w, nil, memory.ErrInvalid)
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		s.memoryResponse(w, nil, memory.ErrInvalid)
		return
	}
	var d memory.Document
	var err error
	if r.Method == http.MethodPut {
		if req.Content == nil {
			s.memoryResponse(w, nil, memory.ErrInvalid)
			return
		}
		d, err = s.deps.Memory.Save(req.Kind, req.Date, *req.Content, req.Revision)
	} else {
		d, err = s.deps.Memory.Delete(req.Kind, req.Date, req.Revision)
	}
	s.memoryResponse(w, d, err)
}

func (s *Server) handleMemoryDaily(w http.ResponseWriter, r *http.Request) {
	if s.deps.Memory == nil {
		writeJSON(w, 503, api.Failure(503, "memory files are unavailable"))
		return
	}
	dates, err := s.deps.Memory.Dates(r.URL.Query().Get("before"), 200)
	next := ""
	if len(dates) == 200 {
		next = dates[len(dates)-1]
	}
	s.memoryResponse(w, map[string]any{"dates": dates, "nextBefore": next, "today": s.deps.Memory.Today()}, err)
}

func (s *Server) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	if s.deps.Memory == nil {
		writeJSON(w, 503, api.Failure(503, "memory files are unavailable"))
		return
	}
	result, err := s.deps.Memory.SearchPage(r.URL.Query().Get("query"), r.URL.Query().Get("before"))
	s.memoryResponse(w, result, err)
}
