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

type rangeMaintenanceStub struct {
	maintenanceStub
	request memoryworker.RangeRequest
	err     error
}

func (m *rangeMaintenanceStub) TriggerRange(r memoryworker.RangeRequest) (memoryworker.Status, error) {
	m.request = r
	return m.Status(), m.err
}
func (m *rangeMaintenanceStub) CancelRange(id string) (memoryworker.Status, error) {
	if id != "known" {
		return m.Status(), memoryworker.ErrRangeInvalid
	}
	return m.Status(), nil
}
func TestMemoryRangeUpdateContract(t *testing.T) {
	m := &rangeMaintenanceStub{}
	s := &Server{deps: Dependencies{MemoryMaintenance: m}}
	for _, tc := range []struct {
		body string
		err  error
		code int
	}{
		{`{"startDate":"2026-10-01","endDate":"2026-10-03","includeArchived":true}`, nil, 202},
		{`{"startDate":"2026-10-01"}`, memoryworker.ErrRangeInvalid, 400},
		{`{"startDate":"2026-10-01","endDate":"2026-10-03"}`, memoryworker.ErrBusy, 409},
		{`null`, nil, 400}, {`[]`, nil, 400}, {`{} {}`, nil, 400}, {`{"modelKey":"other"}`, nil, 400},
	} {
		m.err = tc.err
		w := httptest.NewRecorder()
		s.handleMemoryUpdate(w, httptest.NewRequest("POST", "/api/memory/update", strings.NewReader(tc.body)))
		if w.Code != tc.code {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	if m.calls != 0 {
		t.Fatal("range request used incremental trigger")
	}
	w := httptest.NewRecorder()
	s.handleMemoryCancel(w, httptest.NewRequest("POST", "/api/memory/cancel", strings.NewReader(`{"id":"known"}`)))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	s.handleMemoryCancel(w, httptest.NewRequest("POST", "/api/memory/cancel", strings.NewReader(`{"id":"other"}`)))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
