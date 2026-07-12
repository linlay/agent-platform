package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthRouteIsPublicAndReportsRuntime(t *testing.T) {
	server := &Server{router: http.NewServeMux()}
	server.router.HandleFunc("/healthz", server.method(http.MethodGet, server.handleHealth))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"runtime":"ready"`) || !strings.Contains(body, `"required":false`) {
		t.Fatalf("health body=%s", body)
	}
}
