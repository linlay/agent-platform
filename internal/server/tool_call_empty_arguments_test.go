package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A provider may send a tool call with an ID and name but empty arguments.
// The call is executed, so it and the same-turn text must be persisted with
// the result; otherwise continuation history holds an unpaired tool result.
func TestQueryPersistsToolCallWithEmptyArguments(t *testing.T) {
	var requests [][]any
	modelCalls := 0
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		requests = append(requests, messages)
		modelCalls++
		if modelCalls == 1 {
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"checking the clock"}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_empty","type":"function","function":{"name":"datetime","arguments":""}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
			return
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})

	query := func(body string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	live := query(`{"message":"what time is it"}`)
	for _, eventType := range []string{"content.end", "tool.start", "tool.end", "tool.result"} {
		if !strings.Contains(live, `"type":"`+eventType+`"`) {
			t.Fatalf("expected %s in live stream, got %s", eventType, live)
		}
	}

	files, err := filepath.Glob(filepath.Join(fixture.cfg.Paths.ChatsDir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one chat file, got %v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read chat file: %v", err)
	}
	persisted := string(data)
	if !strings.Contains(persisted, `"id":"call_empty"`) || !strings.Contains(persisted, "checking the clock") {
		t.Fatalf("expected persisted tool call and same-turn text, got %s", persisted)
	}

	requests = nil
	chatID := strings.TrimSuffix(filepath.Base(files[0]), ".jsonl")
	query(`{"chatId":"` + chatID + `","message":"continue"}`)
	if len(requests) != 1 {
		t.Fatalf("expected one continuation model request, got %d", len(requests))
	}
	assertToolResultsPaired(t, requests[0])
}

// History written before the fix holds a tool result whose call was never
// persisted. Continuation must still send a valid request.
func TestQueryContinuationRepairsUnpairedToolResult(t *testing.T) {
	var requests [][]any
	modelCalls := 0
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		messages, _ := payload["messages"].([]any)
		requests = append(requests, messages)
		modelCalls++
		if modelCalls == 1 {
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_lost","type":"function","function":{"name":"datetime","arguments":"{}"}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
			return
		}
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	})
	query := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	}
	query(`{"message":"what time is it"}`)

	files, err := filepath.Glob(filepath.Join(fixture.cfg.Paths.ChatsDir, "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one chat file, got %v err=%v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read chat file: %v", err)
	}
	// Reproduce the damaged shape: drop the line holding the assistant call.
	var kept []string
	dropped := 0
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.Contains(line, `"tool_calls"`) {
			dropped++
			continue
		}
		kept = append(kept, line)
	}
	if dropped != 1 {
		t.Fatalf("expected exactly one persisted tool call line, dropped %d", dropped)
	}
	if err := os.WriteFile(files[0], []byte(strings.Join(kept, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite chat file: %v", err)
	}

	requests = nil
	chatID := strings.TrimSuffix(filepath.Base(files[0]), ".jsonl")
	query(`{"chatId":"` + chatID + `","message":"continue"}`)
	if len(requests) != 1 {
		t.Fatalf("expected one continuation model request, got %d", len(requests))
	}
	assertToolResultsPaired(t, requests[0])
}

func assertToolResultsPaired(t *testing.T, messages []any) {
	t.Helper()
	results := 0
	for index, item := range messages {
		message, _ := item.(map[string]any)
		if message["role"] != "tool" {
			continue
		}
		results++
		if index == 0 {
			t.Fatalf("tool result has no preceding message: %#v", messages)
		}
		previous, _ := messages[index-1].(map[string]any)
		calls, _ := previous["tool_calls"].([]any)
		paired := false
		for _, rawCall := range calls {
			call, _ := rawCall.(map[string]any)
			if call["id"] == message["tool_call_id"] {
				paired = true
			}
		}
		if !paired {
			encoded, _ := json.Marshal(messages)
			t.Fatalf("tool result %v has no matching call: %s", message["tool_call_id"], encoded)
		}
	}
	if results == 0 {
		t.Fatalf("expected a tool result in the request, got %#v", messages)
	}
}
