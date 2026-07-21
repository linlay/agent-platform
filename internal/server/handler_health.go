package server

import (
	"context"
	"net/http"
	"time"

	"agent-platform/internal/api"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	type healthData struct {
		Runtime string `json:"runtime"`
		KBASE   any    `json:"kbase"`
	}
	kbaseState := map[string]any{"required": false}
	if s != nil && s.deps.KBase != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		required, state, err := s.deps.KBase.ProbeSidecar(ctx)
		cancel()
		kbaseState = map[string]any{"required": required, "sidecar": state}
		if err != nil {
			kbaseState["error"] = err.Error()
			kbaseState["degraded"] = true
			if required {
				writeJSON(w, http.StatusServiceUnavailable, api.Failure(1, "unhealthy", map[string]any{"kbase": kbaseState}))
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, api.Success(healthData{Runtime: "ready", KBASE: kbaseState}))
}
