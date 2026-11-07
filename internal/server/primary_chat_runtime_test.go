package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/config"
)

func TestGeneralPrimaryRunPublishesExactlySixToolsWithoutTaskMount(t *testing.T) {
	requests := make(chan map[string]any, 1)
	f := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"ready"},"finish_reason":"stop"}]}`, `[DONE]`)
	}, testFixtureOptions{configure: func(cfg *config.Config) {
		cfg.PrimaryChat = config.PrimaryChatConfig{Enabled: true, SystemPrompt: "Coordinate workers"}
	}})
	rec := postPrimaryQuery(t, f.server, map[string]any{"agentKey": "mock-agent", "message": "hello", "primary": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("query status=%d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	select {
	case body = <-requests:
	default:
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
	var capability struct {
		Data struct {
			Primary struct {
				Enabled bool     `json:"enabled"`
				Keys    []string `json:"agentKeys"`
			} `json:"primaryChat"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &capability); err != nil || !capability.Data.Primary.Enabled || !reflect.DeepEqual(capability.Data.Primary.Keys, []string{"mock-agent"}) {
		t.Fatalf("capabilities=%s err=%v", rec.Body.String(), err)
	}
}

func TestGeneralPrimaryRunAnnouncesProgressWithoutTaskMount(t *testing.T) {
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
		cfg.PrimaryChat = config.PrimaryChatConfig{Enabled: true, SystemPrompt: "Coordinate workers"}
		cfg.ModeDefaultDetails = allModesDetail("simple")
	}})
	rec := &primaryAnnouncementRecorder{ResponseRecorder: httptest.NewRecorder(), announced: announced}
	req := httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"agentKey":"mock-agent","message":"hello","primary":true}`))
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
