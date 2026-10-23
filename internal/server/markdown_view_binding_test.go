package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
)

func TestMarkdownViewBlocksBindFrozenSnapshotsAndReplay(t *testing.T) {
	var calls atomic.Int32
	secondRequest := make(chan string, 1)
	chunk := func(text string) string {
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}}}})
		return string(data)
	}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			body := new(bytes.Buffer)
			_, _ = body.ReadFrom(r.Body)
			secondRequest <- body.String()
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
			return
		}
		// The closing fence arrives in a later chunk than the body.
		writeProviderSSE(t, w,
			chunk("Orders:\n```view key=crm/orders\n{\"items\":[1]}\n``"),
			chunk("`\n```view key=crm/missing\n{}\n```\n```view key=crm/orders\n{\"items\":\"bad\"}\n```"),
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{setupRuntime: func(_ string, cfg *config.Config) {
		dir := filepath.Join(cfg.Paths.EffectiveConnectorsCenterDir(), "crm")
		for name, content := range map[string]string{
			"connector.json":    `{"id":"crm","name":"CRM","version":"1.0.0","type":"view","authMode":"self"}`,
			"view.json":         `{"views":{"orders":{"description":"List orders.","renderer":"html","usage":["display"],"entry":"views/orders.html","dataSchema":{"type":"object","properties":{"items":{"type":"array"}}}}}}`,
			"views/orders.html": "<p>orders v1</p>",
		} {
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, []byte("\nconnectorConfig:\n  connectors:\n    - crm\n")...)
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"message":"show orders","agentKey":"mock-agent"}`))
	req.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("query: %d %s", rec.Code, rec.Body.String())
	}
	var live []map[string]any
	chatID := ""
	for _, message := range decodeSSEMessages(t, rec.Body.String()) {
		if message["type"] == "content.view" {
			live = append(live, message)
		}
		if message["type"] == "chat.start" {
			chatID, _ = message["chatId"].(string)
		}
	}
	if len(live) != 3 {
		t.Fatalf("content.view events: %#v", live)
	}
	first, _ := live[0]["view"].(map[string]any)
	hash, _ := first["hash"].(string)
	if live[0]["index"] != float64(0) || hash == "" || live[0]["viewError"] != nil {
		t.Fatalf("first binding: %#v", live[0])
	}
	if live[1]["viewError"] != "view_not_found" || live[2]["viewError"] != "view_data_invalid" {
		t.Fatalf("error bindings: %#v %#v", live[1], live[2])
	}

	// History reads the frozen template even after the package changes.
	if err := os.WriteFile(filepath.Join(fixture.cfg.Paths.EffectiveConnectorsCenterDir(), "crm", "views", "orders.html"), []byte("<p>orders v2</p>"), 0644); err != nil {
		t.Fatal(err)
	}
	viewRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(viewRec, httptest.NewRequest(http.MethodGet, "/api/view?source=connector&chatId="+chatID+"&connectorId=crm&key=orders&hash="+hash, nil))
	if viewRec.Code != http.StatusOK || !strings.Contains(viewRec.Body.String(), "orders v1") {
		t.Fatalf("frozen view: %d %s", viewRec.Code, viewRec.Body.String())
	}

	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID, nil))
	var chat api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chat); err != nil {
		t.Fatal(err)
	}
	var replayed []any
	for _, event := range chat.Data.Events {
		if event.Type == "content.snapshot" {
			replayed = viewBindingsAsAny(event.Value("views"))
		}
	}
	if len(replayed) != 3 || replayed[0].(map[string]any)["view"].(map[string]any)["hash"] != hash {
		t.Fatalf("replayed views: %#v", replayed)
	}

	// Bindings never reach the model in later turns.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"message":"again","agentKey":"mock-agent","chatId":"`+chatID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(rec, req)
	body := <-secondRequest
	if !strings.Contains(body, "view key=crm/orders") || strings.Contains(body, hash) || strings.Contains(body, `"views"`) {
		t.Fatalf("model history leaked bindings or lost text: %s", body)
	}
}

func viewBindingsAsAny(value any) []any {
	data, _ := json.Marshal(value)
	var out []any
	_ = json.Unmarshal(data, &out)
	return out
}
