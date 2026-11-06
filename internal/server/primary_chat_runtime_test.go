package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestGeneralPrimaryRunPublishesExactlyFiveToolsWithoutTaskMount(t *testing.T) {
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
	want := contracts.PrimaryChatTools()
	sort.Strings(names)
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("actual model tools=%v want=%v", names, want)
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
