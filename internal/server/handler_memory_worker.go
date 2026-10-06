package server

import (
	"encoding/json"
	"errors"
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
	type rangeParams struct {
		StartDate       *string `json:"startDate"`
		EndDate         *string `json:"endDate"`
		IncludeArchived *bool   `json:"includeArchived"`
	}
	params := &rangeParams{}
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&params)
		if err != io.EOF && (err != nil || params == nil || decoder.Decode(&struct{}{}) != io.EOF) {
			writeJSON(w, 400, api.Failure(400, "expected an object with startDate, endDate and optional includeArchived"))
			return
		}
	}
	var status memoryworker.Status
	var err error
	if params.StartDate != nil || params.EndDate != nil || params.IncludeArchived != nil {
		request := memoryworker.RangeRequest{}
		if params.StartDate != nil {
			request.StartDate = *params.StartDate
		}
		if params.EndDate != nil {
			request.EndDate = *params.EndDate
		}
		if params.IncludeArchived != nil {
			request.IncludeArchived = *params.IncludeArchived
		}
		rangeWorker, ok := s.deps.MemoryMaintenance.(interface {
			TriggerRange(memoryworker.RangeRequest) (memoryworker.Status, error)
		})
		if !ok {
			writeJSON(w, 503, api.Failure(503, "date-range memory maintenance unavailable"))
			return
		}
		status, err = rangeWorker.TriggerRange(request)
	} else {
		status, err = s.deps.MemoryMaintenance.Trigger()
	}
	if errors.Is(err, memoryworker.ErrRangeInvalid) {
		writeJSON(w, 400, api.Failure(400, "startDate and endDate must be valid dates, ordered and no later than today"))
		return
	}
	if errors.Is(err, memoryworker.ErrBusy) {
		writeJSON(w, 409, api.Failure(409, "memory maintenance already running"))
		return
	}
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

func (s *Server) handleMemoryCancel(w http.ResponseWriter, r *http.Request) {
	worker, ok := s.deps.MemoryMaintenance.(interface {
		CancelRange(string) (memoryworker.Status, error)
	})
	if !ok {
		writeJSON(w, 503, api.Failure(503, "memory maintenance unavailable"))
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || req.ID == "" || decoder.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, 400, api.Failure(400, "expected a manual task id"))
		return
	}
	status, err := worker.CancelRange(req.ID)
	if errors.Is(err, memoryworker.ErrRangeInvalid) {
		writeJSON(w, 404, api.Failure(404, "manual memory task not found"))
		return
	}
	if err != nil {
		writeJSON(w, 503, api.Failure(503, "unable to cancel memory task"))
		return
	}
	writeJSON(w, 200, api.Success(status))
}
