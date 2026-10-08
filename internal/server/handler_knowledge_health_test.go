package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform/internal/knowledge"
)

type knowledgeHealthService struct {
	handlerKBaseService
	required bool
	state    knowledge.RuntimeState
	err      error
}

func (s *knowledgeHealthService) ProbeRuntime(context.Context) (bool, knowledge.RuntimeState, error) {
	return s.required, s.state, s.err
}

func TestKnowledgeHealthKeepsSidecarJSONAndRequiredSemantics(t *testing.T) {
	for _, tc := range []struct {
		name      string
		required  bool
		available bool
		status    int
	}{
		{"ready", true, true, http.StatusOK},
		{"required unavailable", true, false, http.StatusServiceUnavailable},
		{"optional unavailable", false, false, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &knowledgeHealthService{required: tc.required, state: knowledge.RuntimeState{Engine: "kbx", Available: tc.available}}
			if !tc.available {
				service.err = errors.New("managed KBX unavailable")
				service.state.LastError = service.err.Error()
			}
			rr := httptest.NewRecorder()
			(&Server{deps: Dependencies{KBase: service}}).handleHealth(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if rr.Code != tc.status {
				t.Fatalf("health status = %d, want %d: %s", rr.Code, tc.status, rr.Body.String())
			}
			var response struct {
				Code int
				Data json.RawMessage
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			payload := response.Data
			if tc.status != http.StatusOK {
				var failure struct{ Error json.RawMessage }
				if err := json.Unmarshal(payload, &failure); err != nil {
					t.Fatal(err)
				}
				payload = failure.Error
				if response.Code != 1 {
					t.Fatalf("health failure code changed: %s", rr.Body.String())
				}
			} else if response.Code != 0 {
				t.Fatalf("health success code changed: %s", rr.Body.String())
			}
			var data struct {
				KBASE struct {
					Required bool                       `json:"required"`
					Sidecar  map[string]json.RawMessage `json:"sidecar"`
					Degraded bool                       `json:"degraded"`
				} `json:"kbase"`
			}
			if err := json.Unmarshal(payload, &data); err != nil {
				t.Fatal(err)
			}
			state := data.KBASE.Sidecar
			if data.KBASE.Required != tc.required || string(state["engine"]) != `"kbx"` || string(state["available"]) != map[bool]string{true: "true", false: "false"}[tc.available] || data.KBASE.Degraded != !tc.available {
				t.Fatalf("health contract changed: %s", response.Data)
			}
			if _, exists := state["lancedbVersion"]; exists {
				t.Fatalf("KBX must omit unavailable runtime version: %s", response.Data)
			}
		})
	}
}
