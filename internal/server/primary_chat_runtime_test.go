package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
)

func TestBotPrimaryRunPublishesExactlySixToolsWithoutTaskMount(t *testing.T) {
	requests := make(chan map[string]any, 1)
	f := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ready"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{configure: func(cfg *config.Config) {
		cfg.Bot = config.BotConfig{SystemPrompt: "Coordinate workers"}
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(content), "mode: GENERAL", "mode: BOT", 1)), 0600); err != nil {
			t.Fatal(err)
		}
	}})
	bindUserMailbox(t, f)
	if def, _ := f.registry.AgentDefinition("mock-agent"); def.Mode != "BOT" {
		t.Fatalf("fixture mode=%s", def.Mode)
	}
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(`{"requestId":"bot-first","agentKey":"mock-agent","message":"hello"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("query status=%d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	select {
	case body = <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("model request missing")
	}
	var names []string
	if tools, ok := body["tools"].([]any); ok {
		for _, raw := range tools {
			tool, _ := raw.(map[string]any)
			fn, _ := tool["function"].(map[string]any)
			name, _ := fn["name"].(string)
			names = append(names, name)
		}
	}
	want := []string{"chat_send", "chat_submit", "chat_get_status", "chat_interrupt", "chat_query", "action_announce"}
	sort.Strings(names)
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("actual model tools=%v want=%v", names, want)
	}
	modelInput, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Coordinate workers", "approval_requires_human", "mail_hop_limit", "files:[path]", "default or omission inherits"} {
		if !strings.Contains(string(modelInput), required) {
			t.Fatalf("actual model input missing scheduling instructions %q", required)
		}
	}
	if strings.Contains(string(modelInput), "Read the task-control skill before use") || strings.Contains(string(modelInput), "See its skill.") {
		t.Fatal("primary model still receives an impossible skill-read prerequisite")
	}
	rec = httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/capabilities", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("retired capabilities status=%d", rec.Code)
	}
}

func TestBotPrimaryRunAnnouncesProgressWithoutTaskMount(t *testing.T) {
	var calls atomic.Int32
	announced := make(chan struct{})
	f := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeProviderSSE(t, w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"announce-progress","type":"function","function":{"name":"action_announce","arguments":"{\"message\":\"正在安排测试任务\"}"}}]},"finish_reason":"tool_calls"}]}`, `[DONE]`)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		messages, _ := json.Marshal(body["messages"])
		if !strings.Contains(string(messages), "announced") {
			t.Error("primary announce did not produce a tool result")
		}
		// Announcements are ephemeral. Keep the model turn open until the
		// client observes progress instead of racing final-output cleanup.
		select {
		case <-announced:
		case <-time.After(3 * time.Second):
			t.Error("client did not receive the primary progress announcement")
		}
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ready"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{configure: func(cfg *config.Config) {
		cfg.Bot = config.BotConfig{SystemPrompt: "Coordinate workers"}
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(content), "mode: GENERAL", "mode: BOT", 1)), 0600); err != nil {
			t.Fatal(err)
		}
		cfg.ModeDefaultDetails = allModesDetail("simple")
	}})
	if def, _ := f.registry.AgentDefinition("mock-agent"); def.Mode != "BOT" {
		t.Fatalf("fixture mode=%s", def.Mode)
	}
	rec := &primaryAnnouncementRecorder{ResponseRecorder: httptest.NewRecorder(), announced: announced}
	seedAgentModeChat(t, f.chats.(*chat.FileStore), "bot-main", "loyw3v2a", "mock-agent", "BOT", 1000)
	if _, err := f.chats.(chat.PrimaryStore).SetChatPrimary("mock-agent", "bot-main"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"chatId":"bot-main","agentKey":"mock-agent","message":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	f.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("query status=%d calls=%d: %s", rec.Code, calls.Load(), rec.Body.String())
	}
	for _, want := range []string{"run.activity", "正在安排测试任务", "ready"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("primary announce response missing %q: %s", want, rec.Body.String())
		}
	}
}

type primaryAnnouncementRecorder struct {
	*httptest.ResponseRecorder
	announced chan struct{}
	once      sync.Once
}

func (r *primaryAnnouncementRecorder) Write(data []byte) (int, error) {
	n, err := r.ResponseRecorder.Write(data)
	if strings.Contains(r.Body.String(), "正在安排测试任务") {
		r.once.Do(func() { close(r.announced) })
	}
	return n, err
}

func TestBotEntryRestrictionsAndMainChatRouting(t *testing.T) {
	f := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{configure: func(cfg *config.Config) {
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(content), "mode: GENERAL", "mode: BOT", 1)), 0600); err != nil {
			t.Fatal(err)
		}
	}})
	bindUserMailbox(t, f)
	for _, body := range []string{
		`{"agentKey":"mock-agent","message":"new"}`,
		`{"agentKey":"mock-agent","message":"new","primary":true}`,
	} {
		rec := postPrimaryQuery(t, f.server, mapFromBotJSON(t, body))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query creation status=%d: %s", rec.Code, rec.Body.String())
		}
	}
	// Concurrent first messages must share one newly-created main Chat.
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			f.server.handleSend(rec, httptest.NewRequest(http.MethodPost, "/api/send", strings.NewReader(fmt.Sprintf(`{"requestId":"bot-concurrent-%d","agentKey":"mock-agent","message":"one"}`, i))))
			if rec.Code != http.StatusOK {
				t.Errorf("concurrent send: %d %s", rec.Code, rec.Body.String())
				return
			}
			var result struct {
				Data struct {
					ChatID string `json:"chatId"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Error(err)
				return
			}
			ids <- result.Data.ChatID
		}(i)
	}
	wg.Wait()
	close(ids)
	mainID := ""
	for id := range ids {
		if mainID != "" && mainID != id {
			t.Fatalf("multiple main Chats: %s %s", mainID, id)
		}
		mainID = id
	}
	status, first := sendHTTP(t, f, `{"requestId":"bot-a","agentKey":"mock-agent","message":"one"}`)
	if status != http.StatusOK || first.ChatID == "" {
		t.Fatalf("first=%+v status=%d", first, status)
	}
	status, second := sendHTTP(t, f, `{"requestId":"bot-b","agentKey":"mock-agent","message":"two"}`)
	if status != http.StatusOK || second.ChatID != first.ChatID {
		t.Fatalf("second=%+v first=%+v status=%d", second, first, status)
	}
	status, _ = sendHTTP(t, f, `{"requestId":"bot-plan","agentKey":"mock-agent","message":"plan","planningMode":true}`)
	if status != http.StatusBadRequest {
		t.Fatalf("planning accepted: %d", status)
	}
	for _, chatID := range []string{"", first.ChatID} {
		rec := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]any{"agentKey": "mock-agent", "chatId": chatID, "message": "execute"})
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/execute", strings.NewReader(string(body))))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("execute accepted: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func mapFromBotJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBotFirstUploadReservationBecomesMainChat(t *testing.T) {
	f := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{configure: func(cfg *config.Config) {
		path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(source), "mode: GENERAL", "mode: BOT", 1)), 0600); err != nil {
			t.Fatal(err)
		}
	}})
	if _, _, err := f.chats.EnsureChat("upload-staging", "", ""); err != nil {
		t.Fatal(err)
	}
	status, receipt := sendHTTP(t, f, `{"requestId":"first-upload","chatId":"upload-staging","agentKey":"mock-agent","message":"read file"}`)
	main, err := f.chats.(chat.PrimaryStore).PrimaryChatID("mock-agent")
	if status != 200 || receipt.ChatID != "upload-staging" || main != receipt.ChatID || err != nil {
		t.Fatalf("upload reservation: %d %+v main=%s err=%v", status, receipt, main, err)
	}
	if _, _, err := f.chats.EnsureChat("late-upload", "", ""); err != nil {
		t.Fatal(err)
	}
	status, _ = sendHTTP(t, f, `{"requestId":"late-upload","chatId":"late-upload","agentKey":"mock-agent","message":"read next"}`)
	if status != 409 {
		t.Fatalf("upload race status=%d", status)
	}
}
