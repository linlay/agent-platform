package server

import (
	"encoding/json"
	"io"
	"net/http"

	"agent-platform/internal/api"
	"agent-platform/internal/memoryworker"
)

type MemoryMaintenance interface {
	Trigger() (memoryworker.Status, error)
	Status() memoryworker.Status
}

func (s *Server) handleMemoryUpdate(w http.ResponseWriter, r *http.Request) {
	if s.deps.MemoryMaintenance == nil {
		writeJSON(w, 503, api.Failure(503, "memory maintenance unavailable"))
		return
	}
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		var params struct{}
		err := decoder.Decode(&params)
		if err != io.EOF && (err != nil || decoder.Decode(&struct{}{}) != io.EOF) {
			writeJSON(w, 400, api.Failure(400, "expected an empty JSON object"))
			return
		}
	}
	status, err := s.deps.MemoryMaintenance.Trigger()
	if err != nil {
		writeJSON(w, 503, api.Failure(503, "memory maintenance unavailable"))
		return
	}
	writeJSON(w, http.StatusAccepted, api.Success(map[string]any{"accepted": true, "status": status}))
}
func (s *Server) handleMemoryStatus(w http.ResponseWriter, r *http.Request) {
	if s.deps.MemoryMaintenance == nil {
		writeJSON(w, 503, api.Failure(503, "memory maintenance unavailable"))
		return
	}
	writeJSON(w, 200, api.Success(s.deps.MemoryMaintenance.Status()))
}
