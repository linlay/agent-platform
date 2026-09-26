package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/memory"
	"agent-platform/internal/timecontract"
	"agent-platform/internal/ws"
)

type scopeCountingStore struct {
	*memory.SQLiteStore
	lists  atomic.Int32
	failAt int32
}

func (s *scopeCountingStore) ListAll(agentKey string) ([]api.StoredMemoryResponse, error) {
	if s.lists.Add(1) == s.failAt {
		return nil, &timecontract.Violation{Field: "updatedAt", Location: "memory.sqlite.row[bad].updatedAt", Reason: "invalid timestamp"}
	}
	return s.SQLiteStore.ListAll(agentKey)
}

func TestMemoryScopeTimeErrorsAcrossTransports(t *testing.T) {
	for _, route := range []string{"detail", "save"} {
		for _, failAt := range []int32{1, 2} {
			if route == "detail" && failAt == 2 {
				continue
			}
			fixture := newMemoryEnabledTestFixture(t)
			store := &scopeCountingStore{SQLiteStore: fixture.memories.(*memory.SQLiteStore), failAt: failAt}
			fixture.server.deps.Memory = store
			payload := map[string]any{"agentKey": "mock-agent", "scopeType": "agent", "mode": "records", "records": []any{}}
			body, _ := json.Marshal(payload)
			rec := httptest.NewRecorder()
			if route == "detail" {
				fixture.server.handleMemoryScope(rec, httptest.NewRequest(http.MethodGet, "/api/memory/scope/detail?agentKey=mock-agent&scopeType=agent", nil))
			} else {
				fixture.server.handleMemoryScopeSave(rec, httptest.NewRequest(http.MethodPost, "/api/memory/scope/save", bytes.NewReader(body)))
			}
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "time_contract_violation") {
				t.Fatalf("HTTP %s read %d: %d %s", route, failAt, rec.Code, rec.Body.String())
			}
			store.lists.Store(0)
			enableMemoryFixtureWebSocket(t, fixture.server)
			conn := dialMemoryWebSocket(t, fixture.server)
			if err := conn.WriteJSON(ws.RequestFrame{Frame: ws.FrameRequest, Type: "/api/memory/scope/" + route, ID: "bad-time", Payload: marshalPayload(payload)}); err != nil {
				t.Fatal(err)
			}
			var frame ws.ResponseFrame
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
			if frame.Frame != ws.FrameError || frame.Code != http.StatusUnprocessableEntity || frame.Type != "time_contract_violation" {
				t.Fatalf("WS %s read %d: %#v", route, failAt, frame)
			}
		}
	}
}
