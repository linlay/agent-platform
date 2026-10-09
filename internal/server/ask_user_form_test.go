package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/config"
)

func TestAskUserFormSubmitProtocolAndSiblingWait(t *testing.T) {
	var calls atomic.Int32
	modelMessages := make(chan []map[string]any, 1)
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			args, _ := json.Marshal(map[string]any{"title": "Profile", "html": `<label>Name<input name="name" required></label>`})
			event, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
				map[string]any{"index": 0, "id": "sibling", "type": "function", "function": map[string]any{"name": "datetime", "arguments": "{}"}},
				map[string]any{"index": 1, "id": "form", "type": "function", "function": map[string]any{"name": "ask_user_form", "arguments": string(args)}},
			}}, "finish_reason": "tool_calls"}}})
			writeProviderSSE(t, w, string(event), `[DONE]`)
		} else {
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				return
			}
			modelMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `[DONE]`)
		}
	}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.Replace(raw, []byte("    - ask_user_question"), []byte("    - ask_user_question\n    - ask_user_form"), 1)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}})
	server := newLoopbackServer(t, fixture.server)
	defer server.Close()
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Post(server.URL+"/api/query", "application/json", strings.NewReader(`{"message":"collect profile"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	var before strings.Builder
	var ask map[string]any
	for {
		line, err := reader.ReadString('\n')
		before.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			event := decodeSSELine(t, line)
			if event["type"] == "tool.result" {
				t.Fatalf("sibling executed before form submit: %s", before.String())
			}
			if event["type"] == "awaiting.ask" {
				ask = event
				break
			}
		}
		if err != nil {
			t.Fatalf("no ask: %v %s", err, before.String())
		}
	}
	if ask["mode"] != "form" || ask["view"].(map[string]any)["key"] != "ask_user_form" {
		t.Fatalf("unexpected ask %#v", ask)
	}
	submit := func(field string, value any, want int) {
		t.Helper()
		payload := map[string]any{"agentKey": "mock-agent", "runId": ask["runId"], "awaitingId": ask["awaitingId"], field: value}
		raw, _ := json.Marshal(payload)
		recorder := httptest.NewRecorder()
		fixture.server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewReader(raw)))
		if recorder.Code != want {
			t.Fatalf("submit want %d got %d %s", want, recorder.Code, recorder.Body.String())
		}
	}
	submit("params", []any{map[string]any{"decision": "approve", "data": map[string]any{}}}, 400)
	submit("param", map[string]any{"decision": "approve"}, 400)
	param := map[string]any{"decision": "approve", "data": map[string]any{"name": "Alice", "undeclared": "do not send to model"}}
	submit("param", param, 200)
	submit("param", param, 409)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	body := before.String() + string(rest)
	if strings.Count(body, `"type":"awaiting.ask"`) != 1 || !strings.Contains(body, `"type":"awaiting.answer"`) {
		t.Fatalf("unexpected stream %s", body)
	}
	select {
	case messages := <-modelMessages:
		found := false
		for _, message := range messages {
			if message["role"] == "tool" && message["tool_call_id"] == "form" {
				text, _ := message["content"].(string)
				if !strings.Contains(text, "Alice") || strings.Contains(text, "undeclared") {
					t.Fatalf("bad model output %s", text)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("form result missing %#v", messages)
		}
	default:
		t.Fatal("no next model call")
	}
}
