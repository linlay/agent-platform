package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/kbase"
)

// The handler tests the service contract, independently of the knowledge engine.
// KBX execution and maintenance availability are covered in internal/kbx.
type handlerKBaseService struct {
	calls          []string
	validateErr    error
	statusResult   kbase.Status
	statusErr      error
	refreshErr     error
	refreshOptions kbase.RefreshOptions
}

var _ KBaseService = (*handlerKBaseService)(nil)

func (s *handlerKBaseService) ValidateAgent(agentKey string) error {
	s.calls = append(s.calls, "validate:"+agentKey)
	if agentKey != "docs" {
		return &kbase.PolicyError{Kind: kbase.ErrorNotFound, Message: "private catalog diagnostic"}
	}
	return s.validateErr
}

func (s *handlerKBaseService) Status(agentKey string) (kbase.Status, error) {
	s.calls = append(s.calls, "status:"+agentKey)
	return s.statusResult, s.statusErr
}

func (s *handlerKBaseService) Refresh(_ context.Context, agentKey string, options kbase.RefreshOptions) (kbase.RefreshResult, error) {
	s.calls = append(s.calls, "refresh:"+agentKey)
	s.refreshOptions = options
	return kbase.RefreshResult{AgentKey: agentKey, Mode: options.Mode, Status: "success"}, s.refreshErr
}

func (*handlerKBaseService) ProbeSidecar(context.Context) (bool, kbase.LanceEngineState, error) {
	panic("HTTP status/refresh must not probe the knowledge engine")
}

func (*handlerKBaseService) ReconcileWatchers(context.Context) {
	panic("HTTP status/refresh must not reconcile knowledge watchers")
}

func TestHandleKBaseStatusMappingAndMethods(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		body      string
		want      int
		allow     string
		wantCalls string
		wantForce bool
	}{
		{name: "status", method: http.MethodGet, path: "/api/kbase/docs/status", want: http.StatusOK, wantCalls: "validate:docs,status:docs"},
		{name: "refresh", method: http.MethodPost, path: "/api/kbase/docs/refresh", body: `{}`, want: http.StatusOK, wantCalls: "validate:docs,refresh:docs"},
		{name: "forced refresh", method: http.MethodPost, path: "/api/kbase/docs/refresh", body: `{"force":true}`, want: http.StatusOK, wantCalls: "validate:docs,refresh:docs", wantForce: true},
		{name: "empty refresh body", method: http.MethodPost, path: "/api/kbase/docs/refresh", want: http.StatusOK, wantCalls: "validate:docs,refresh:docs"},
		{name: "unknown agent", method: http.MethodGet, path: "/api/kbase/missing/status", want: http.StatusNotFound, wantCalls: "validate:missing"},
		{name: "disabled capability", method: http.MethodGet, path: "/api/kbase/disabled/status", want: http.StatusNotFound, wantCalls: "validate:disabled"},
		{name: "unknown agent precedes method", method: http.MethodPost, path: "/api/kbase/missing/status", want: http.StatusNotFound, wantCalls: "validate:missing"},
		{name: "disabled capability precedes method", method: http.MethodPost, path: "/api/kbase/disabled/status", want: http.StatusNotFound, wantCalls: "validate:disabled"},
		{name: "bad path", method: http.MethodGet, path: "/api/kbase/docs", want: http.StatusNotFound},
		{name: "unknown action", method: http.MethodGet, path: "/api/kbase/docs/unknown", want: http.StatusNotFound, wantCalls: "validate:docs"},
		{name: "status method", method: http.MethodPost, path: "/api/kbase/docs/status", want: http.StatusMethodNotAllowed, allow: http.MethodGet, wantCalls: "validate:docs"},
		{name: "refresh method", method: http.MethodGet, path: "/api/kbase/docs/refresh", want: http.StatusMethodNotAllowed, allow: http.MethodPost, wantCalls: "validate:docs"},
		{name: "invalid body", method: http.MethodPost, path: "/api/kbase/docs/refresh", body: `{`, want: http.StatusBadRequest, wantCalls: "validate:docs"},
		{name: "invalid force type", method: http.MethodPost, path: "/api/kbase/docs/refresh", body: `{"force":"yes"}`, want: http.StatusBadRequest, wantCalls: "validate:docs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &handlerKBaseService{statusResult: kbase.Status{
				AgentKey: "docs", Mode: kbase.Mode, Engine: "kbx", Files: 3,
				Stale: true, Degraded: true, Error: "index maintenance unavailable",
			}}
			srv := &Server{deps: Dependencies{KBase: service}}
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rr := httptest.NewRecorder()
			srv.handleKBase(rr, req)
			response := assertKBaseHTTPResponse(t, rr, tt.want)
			if got := rr.Header().Get("Allow"); got != tt.allow {
				t.Fatalf("Allow=%q want=%q", got, tt.allow)
			}
			if got := strings.Join(service.calls, ","); got != tt.wantCalls {
				t.Fatalf("service calls=%q want=%q", got, tt.wantCalls)
			}
			if tt.want != http.StatusOK {
				if strings.Contains(response.Msg, "private catalog diagnostic") {
					t.Fatalf("private validation detail leaked: %s", response.Msg)
				}
				return
			}
			if strings.HasSuffix(tt.path, "/status") {
				// Compare the wire representation: KBX status derives unknown-count
				// fields during encoding, so decoding need not restore the raw DTO.
				want, err := json.Marshal(service.statusResult)
				if err != nil {
					t.Fatal(err)
				}
				if string(response.Data) != string(want) {
					t.Fatalf("status payload=%s want=%s", response.Data, want)
				}
			} else {
				want := kbase.RefreshOptions{Mode: "manual", Force: tt.wantForce}
				if !reflect.DeepEqual(service.refreshOptions, want) {
					t.Fatalf("refresh options=%#v want=%#v", service.refreshOptions, want)
				}
				var result kbase.RefreshResult
				if err := json.Unmarshal(response.Data, &result); err != nil {
					t.Fatal(err)
				}
				if result.AgentKey != "docs" || result.Mode != "manual" || result.Status != "success" {
					t.Fatalf("refresh payload=%#v", result)
				}
			}
		})
	}

	rr := httptest.NewRecorder()
	(&Server{}).handleKBase(rr, httptest.NewRequest(http.MethodGet, "/api/kbase/docs/status", nil))
	response := assertKBaseHTTPResponse(t, rr, http.StatusServiceUnavailable)
	if response.Msg != "kbase is not configured" {
		t.Fatalf("missing service message=%q", response.Msg)
	}
}

func TestHandleKBaseServiceErrors(t *testing.T) {
	unavailable := &kbase.PolicyError{Kind: kbase.ErrorUnavailable, Message: "KBX index maintenance unavailable"}
	tests := []struct {
		name      string
		service   handlerKBaseService
		method    string
		path      string
		want      int
		wantMsg   string
		wantCalls string
	}{
		{name: "validation unavailable", service: handlerKBaseService{validateErr: unavailable}, method: http.MethodGet, path: "/api/kbase/docs/status", want: http.StatusServiceUnavailable, wantMsg: unavailable.Message, wantCalls: "validate:docs"},
		{name: "status unavailable", service: handlerKBaseService{statusErr: unavailable}, method: http.MethodGet, path: "/api/kbase/docs/status", want: http.StatusServiceUnavailable, wantMsg: unavailable.Message, wantCalls: "validate:docs,status:docs"},
		{name: "KBX refresh unavailable", service: handlerKBaseService{refreshErr: unavailable}, method: http.MethodPost, path: "/api/kbase/docs/refresh", want: http.StatusServiceUnavailable, wantMsg: unavailable.Message, wantCalls: "validate:docs,refresh:docs"},
		{name: "agent disappears after validation", service: handlerKBaseService{statusErr: &kbase.PolicyError{Kind: kbase.ErrorNotFound, Message: "private catalog diagnostic"}}, method: http.MethodGet, path: "/api/kbase/docs/status", want: http.StatusNotFound, wantMsg: "agent not found", wantCalls: "validate:docs,status:docs"},
		{name: "invalid refresh", service: handlerKBaseService{refreshErr: &kbase.PolicyError{Kind: kbase.ErrorInvalid, Message: "invalid refresh options"}}, method: http.MethodPost, path: "/api/kbase/docs/refresh", want: http.StatusBadRequest, wantMsg: "invalid refresh options", wantCalls: "validate:docs,refresh:docs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := &Server{deps: Dependencies{KBase: &tt.service}}
			rr := httptest.NewRecorder()
			srv.handleKBase(rr, httptest.NewRequest(tt.method, tt.path, nil))
			response := assertKBaseHTTPResponse(t, rr, tt.want)
			if response.Msg != tt.wantMsg {
				t.Fatalf("message=%q want=%q", response.Msg, tt.wantMsg)
			}
			if got := strings.Join(tt.service.calls, ","); got != tt.wantCalls {
				t.Fatalf("service calls=%q want=%q", got, tt.wantCalls)
			}
		})
	}
}

func assertKBaseHTTPResponse(t *testing.T, rr *httptest.ResponseRecorder, wantStatus int) api.ApiResponse[json.RawMessage] {
	t.Helper()
	if rr.Code != wantStatus {
		t.Fatalf("HTTP status=%d want=%d body=%s", rr.Code, wantStatus, rr.Body.String())
	}
	var response api.ApiResponse[json.RawMessage]
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	wantCode := wantStatus
	if wantStatus == http.StatusOK {
		wantCode = 0
		if response.Msg != "success" {
			t.Fatalf("success message=%q", response.Msg)
		}
	} else if string(response.Data) != "{}" {
		t.Fatalf("failure returned result data: %s", response.Data)
	}
	if response.Code != wantCode {
		t.Fatalf("response code=%d want=%d", response.Code, wantCode)
	}
	return response
}
