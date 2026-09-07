package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

type contextCancelableEngine struct {
	contracts.AgentEngine
	cancels chan context.CancelFunc
}

func (e contextCancelableEngine) Stream(ctx context.Context, req api.QueryRequest, session contracts.QuerySession) (contracts.AgentStream, error) {
	ctx, cancel := context.WithCancel(ctx)
	e.cancels <- cancel
	stream, err := e.AgentEngine.Stream(ctx, req, session)
	if err != nil {
		cancel()
		return nil, err
	}
	return cancelableTestStream{AgentStream: stream, cancel: cancel}, nil
}

type cancelableTestStream struct {
	contracts.AgentStream
	cancel context.CancelFunc
}

func (s cancelableTestStream) Close() error { defer s.cancel(); return s.AgentStream.Close() }

// Exercise the real provider -> Bash -> delta mapper -> StepWriter -> SQLite
// path, then send a new query whose provider request must contain paired calls.
func TestCanceledBashHistoryLoadsAndContinues(t *testing.T) {
	for _, tc := range []struct {
		count        int
		parentCancel bool
	}{{1, false}, {2, false}, {1, true}, {2, true}} {
		t.Run(fmt.Sprintf("tools_%d/context_%t", tc.count, tc.parentCancel), func(t *testing.T) {
			count := tc.count
			command := "echo started; while :; do :; done"
			if runtime.GOOS == "windows" {
				command = "Write-Output started; Start-Sleep -Seconds 30"
			}
			var requests atomic.Int32
			continued := make(chan map[string]any, 1)
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if requests.Add(1) > 1 {
					continued <- request
					writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"continued safely"},"finish_reason":"stop"}]}`, `[DONE]`)
					return
				}
				args, _ := json.Marshal(map[string]any{"command": command, "cwd": "@chat"})
				calls := make([]map[string]any, count)
				for index := range calls {
					calls[index] = map[string]any{"index": index, "id": fmt.Sprintf("cancel-tool-%d", index), "type": "function", "function": map[string]any{"name": "bash", "arguments": string(args)}}
				}
				chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
				writeProviderSSE(t, w, string(chunk), `[DONE]`)
			}, testFixtureOptions{
				configure: func(cfg *config.Config) {
					cfg.ContainerHub.Enabled = false
					cfg.Bash.ShellExecutable = ""
					cfg.Bash.AllowedCommands = []string{"*"}
					cfg.Bash.ShellFeaturesEnabled = true
				},
				setupRuntime: func(_ string, cfg *config.Config) {
					path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					definition := strings.ReplaceAll(string(data), "    - datetime", "    - bash")
					definition = strings.ReplaceAll(definition, "  environmentId: shell", "  environmentId: \"\"")
					if err := os.WriteFile(path, []byte(definition), 0600); err != nil {
						t.Fatal(err)
					}
				},
			})
			cancels := make(chan context.CancelFunc, 2)
			if tc.parentCancel {
				fixture.agent = contextCancelableEngine{AgentEngine: fixture.agent, cancels: cancels}
				fixture.server = newServerFromFixture(t, fixture)
			}
			server := newLoopbackServer(t, fixture.server)
			defer server.Close()
			client := &http.Client{Timeout: 15 * time.Second}
			response, err := client.Post(server.URL+"/api/query", "application/json", strings.NewReader(`{"message":"synthetic cancellation test","agentKey":"mock-agent","accessLevel":"full_access"}`))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			var body strings.Builder
			runID, chatID := "", ""
			started := map[string]bool{}
			for len(started) < count {
				line, err := reader.ReadString('\n')
				body.WriteString(line)
				if err != nil {
					t.Fatalf("no running tools: %v: %s", err, body.String())
				}
				if !strings.HasPrefix(line, "data: {") {
					continue
				}
				value := decodeSSELine(t, line)
				if value["type"] == "run.start" {
					runID, _ = value["runId"].(string)
					chatID, _ = value["chatId"].(string)
				}
				if value["type"] == "tool.output" {
					id, _ := value["toolId"].(string)
					started[id] = true
				}
			}
			terminal := "run.cancel"
			if tc.parentCancel {
				(<-cancels)()
				terminal = "run.error"
			} else {
				interrupt := httptest.NewRecorder()
				fixture.server.ServeHTTP(interrupt, httptest.NewRequest(http.MethodPost, "/api/interrupt", strings.NewReader(fmt.Sprintf(`{"agentKey":"mock-agent","runId":%q}`, runID))))
				if interrupt.Code != http.StatusOK {
					t.Fatalf("interrupt: %s", interrupt.Body.String())
				}
			}
			rest, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			body.Write(rest)
			seen := map[string]int{}
			for _, event := range decodeSSEMessages(t, body.String()) {
				if event["type"] == "tool.result" {
					id, _ := event["toolId"].(string)
					seen[id]++
				}
				if event["type"] == terminal && len(seen) != count {
					t.Fatal("terminal preceded results")
				}
			}
			for id := range started {
				if seen[id] != 1 {
					t.Fatalf("tool %s result count=%d", id, seen[id])
				}
			}
			if !strings.Contains(body.String(), `"type":"`+terminal+`"`) {
				t.Fatalf("missing cancellation: %s", body.String())
			}
			history := httptest.NewRecorder()
			fixture.server.ServeHTTP(history, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID+"&includeRawMessages=true", nil))
			if history.Code != http.StatusOK {
				t.Fatalf("history: %s", history.Body.String())
			}
			jsonl, err := fixture.chats.LoadJSONLContent(chatID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(jsonl, `"_type":"react-tool"`) {
				t.Fatalf("no durable results: %s", jsonl)
			}
			query, _ := json.Marshal(map[string]any{"message": "continue", "agentKey": "mock-agent", "chatId": chatID})
			next := httptest.NewRecorder()
			fixture.server.ServeHTTP(next, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(query)))
			if next.Code != http.StatusOK || !strings.Contains(next.Body.String(), "continued safely") {
				t.Fatalf("continuation: %s", next.Body.String())
			}
			request := <-continued
			paired := map[string]int{}
			for _, raw := range request["messages"].([]any) {
				message := raw.(map[string]any)
				if message["role"] == "tool" {
					id, _ := message["tool_call_id"].(string)
					paired[id]++
				}
			}
			for id := range started {
				if paired[id] != 1 {
					t.Fatalf("continuation lost tool %s: %#v", id, paired)
				}
			}
		})
	}
}

func TestAuditedUnknownToolOutcomeRepairLoadsAndContinues(t *testing.T) {
	providerHistory := make(chan map[string]any, 1)
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		providerHistory <- request
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"verify side effects before retrying"},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	const chatID = "synthetic-audited-recovery"
	persistIncompleteChatHistory(t, fixture.chats, chatID)
	if _, err := fixture.chats.LoadChat(chatID); err == nil {
		t.Fatal("unpaired valid call should still fail closed")
	}
	original, err := fixture.chats.LoadJSONLContent(chatID)
	if err != nil {
		t.Fatal(err)
	}
	recordedAt := time.Now().Add(time.Second).UnixMilli()
	payload, _ := json.Marshal(map[string]any{
		"error": "tool_execution_outcome_unknown", "exitCode": -1, "executionState": "unknown",
		"output":   "Final result and side effects unknown; verify external state before retrying.",
		"reason":   "manual_recovery_missing_result",
		"recovery": map[string]any{"method": "manual_unknown_outcome", "recordedAt": recordedAt, "originalJSONLSHA256": fmt.Sprintf("%x", sha256.Sum256([]byte(original))), "operator": "synthetic-test"},
	})
	if err := fixture.chats.AppendStepLine(chatID, chat.StepLine{
		Type: chat.StepLineTypeReactTool, ChatID: chatID, RunID: chatID + "-run", Seq: 1, UpdatedAt: recordedAt,
		Messages: []chat.StoredMessage{{Role: "tool", Name: "bash", ToolCallID: "call-unknown", ToolID: "call-unknown", Ts: &recordedAt, Content: []chat.ContentPart{{Type: "text", Text: string(payload)}}}},
	}); err != nil {
		t.Fatal(err)
	}
	after, err := fixture.chats.LoadJSONLContent(chatID)
	if err != nil || !strings.HasPrefix(after, original) {
		t.Fatal("repair changed original audit bytes")
	}
	detail, err := fixture.chats.LoadChat(chatID)
	if err != nil {
		t.Fatal(err)
	}
	sawOriginalFailure := false
	for _, event := range detail.Events {
		if event.Type == "run.error" {
			sawOriginalFailure = true
		}
	}
	if !sawOriginalFailure {
		t.Fatal("repair erased original run failure")
	}
	next := httptest.NewRecorder()
	fixture.server.ServeHTTP(next, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(`{"agentKey":"mock-agent","chatId":"`+chatID+`","message":"continue without replay"}`)))
	if next.Code != http.StatusOK || !strings.Contains(next.Body.String(), "verify side effects") {
		t.Fatalf("continuation: %s", next.Body.String())
	}
	request := <-providerHistory
	seen := 0
	for _, raw := range request["messages"].([]any) {
		message := raw.(map[string]any)
		if message["role"] == "tool" && message["tool_call_id"] == "call-unknown" {
			seen++
			if !strings.Contains(fmt.Sprint(message["content"]), "tool_execution_outcome_unknown") {
				t.Fatal("provider lost unknown outcome")
			}
		}
	}
	if seen != 1 {
		t.Fatalf("provider matching result count=%d", seen)
	}
}
