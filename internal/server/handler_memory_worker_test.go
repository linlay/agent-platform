package server

import (
	"agent-platform/internal/memoryworker"
	"net/http/httptest"
	"strings"
	"testing"
)

type maintenanceStub struct{ calls int }

func (m *maintenanceStub) Trigger() (memoryworker.Status, error) { m.calls++; return m.Status(), nil }
func (m *maintenanceStub) Status() memoryworker.Status {
	return memoryworker.Status{Enabled: true, State: "queued", PollIntervalSeconds: 300}
}
func TestMemoryManualUpdateAndStatus(t *testing.T) {
	m := &maintenanceStub{}
	s := &Server{deps: Dependencies{MemoryMaintenance: m}}
	w := httptest.NewRecorder()
	s.handleMemoryUpdate(w, httptest.NewRequest("POST", "/api/memory/update", strings.NewReader("{}")))
	if w.Code != 202 || m.calls != 1 || !strings.Contains(w.Body.String(), "queued") {
		t.Fatal(w.Code, w.Body.String(), m.calls)
	}
	w = httptest.NewRecorder()
	s.handleMemoryStatus(w, httptest.NewRequest("GET", "/api/memory/status", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "300") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleMemoryUpdate(w, httptest.NewRequest("POST", "/api/memory/update", strings.NewReader(`{"root":"/tmp/other"}`)))
	if w.Code != 400 || m.calls != 1 {
		t.Fatal(w.Code, m.calls)
	}
}
