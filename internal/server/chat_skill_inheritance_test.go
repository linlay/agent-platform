package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/config"
)

func TestFollowupQueryRunsPreviouslyReferencedNodeSkillWithoutApproval(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is required for the skill execution integration test")
	}
	var calls atomic.Int32
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		switch calls.Add(1) {
		case 1:
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"Skill selected."},"finish_reason":"stop"}]}`, `[DONE]`)
		case 2:
			args := `{"command":"node scripts/calculate.mjs","cwd":"@skills-center/calculator","description":"Calculate again"}`
			writeProviderSSE(t, w, providerToolCallArgsDeltaFrame(t, "calculate", "bash", args, "tool_calls"), `[DONE]`)
		default:
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"Calculated 42."},"finish_reason":"stop"}]}`, `[DONE]`)
		}
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.ContainerHub.Enabled = false
			cfg.Bash.AllowedCommands = []string{"node"}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			writeTestSkill(t, cfg.Paths.SkillsCenterDir, "calculator")
			scripts := filepath.Join(cfg.Paths.SkillsCenterDir, "calculator", "scripts")
			if err := os.MkdirAll(scripts, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(scripts, "calculate.mjs"), []byte("console.log(42)\n"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			definition := strings.ReplaceAll(string(data), "  environmentId: shell", "  environmentId: \"\"")
			if err := os.WriteFile(path, []byte(definition), 0600); err != nil {
				t.Fatal(err)
			}
		},
	})
	query := func(body string) []map[string]any {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		req := httptest.NewRequest(http.MethodPost, "/api/query?detail=full", strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"type":"run.awaiting"`) || strings.Contains(rec.Body.String(), `"type":"run.error"`) {
			t.Fatalf("query did not complete without HITL: %d %s", rec.Code, rec.Body.String())
		}
		return decodeSSEMessages(t, rec.Body.String())
	}
	first := query(`{"message":"Use the calculator skill","agentKey":"mock-agent","references":[{"type":"skill","id":"calculator"}]}`)
	chatID, _ := findSSEMessageByType(t, first, "run.start")["chatId"].(string)
	if chatID == "" {
		t.Fatal("missing Chat ID")
	}
	body, err := json.Marshal(map[string]any{"message": "Calculate again", "agentKey": "mock-agent", "chatId": chatID})
	if err != nil {
		t.Fatal(err)
	}
	second := query(string(body))
	request := findSSEMessageByType(t, second, "request.query")
	if refs, ok := request["references"].([]any); ok && len(refs) > 0 {
		t.Fatalf("inherited skill added to current request: %+v", request)
	}
	findSSEMessageByType(t, second, "run.complete")
	// Check persisted tool output, proving that both preflight and launch-time
	// validation allowed the real Node process without an approval receipt.
	detail, err := fixture.chats.LoadChat(chatID)
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, message := range detail.RawMessages {
		if message["role"] != "tool" {
			continue
		}
		raw, _ := json.Marshal(message["content"])
		if strings.Contains(string(raw), "bash-access:skill-script") && strings.Contains(string(raw), "42") && !strings.Contains(string(raw), "approvalSource") {
			matched = true
		}
	}
	if !matched || calls.Load() != 3 {
		t.Fatalf("Node did not execute with inherited skill grant: calls=%d messages=%+v", calls.Load(), detail.RawMessages)
	}
}
