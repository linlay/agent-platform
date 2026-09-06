package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	runtimetypes "agent-platform/internal/runtime/types"
)

// These are independent entry points, with the same deterministic Agent input.
// Assert the persisted truth as well as each entry point's response contract.
func TestQueryExecutionEntryPointParity(t *testing.T) {
	for _, outcome := range []string{"success", "start-error", "execution-error", "cancel", "team", "subagent", "recovery"} {
		for _, entry := range []string{"sse", "json", "runtime", "legacy", "callback"} {
			t.Run(outcome+"/"+entry, func(t *testing.T) {
				fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
					t.Error("stub executor unexpectedly called the model")
				}, testFixtureOptions{setupRuntime: func(root string, cfg *config.Config) {
					setupOrchestratedTeamRuntime(t)(root, cfg)
					path := filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if outcome == "recovery" {
						data = append(data, []byte("\nmode: PLAN-EXECUTE\n")...)
					}
					if outcome == "subagent" {
						data = []byte(strings.Replace(string(data), "    - datetime", "    - datetime\n    - agent_invoke", 1))
					}
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				}})
				main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{
					contracts.DeltaContent{Text: "final answer"},
					contracts.DeltaUsageSnapshot{RunPromptTokens: 7, RunCompletionTokens: 3, RunTotalTokens: 10},
				}}
				engine := &orchestratorAgentEngine{streams: []contracts.AgentStream{main}}
				expectedReason, expectedText, expectedTokens := "complete", "final answer", 10
				req := api.QueryRequest{AgentKey: "mock-agent", ChatID: "parity-chat", RunID: "parity-run", Message: "hello", IncludeUsage: true, IncludeFullText: true}
				switch outcome {
				case "start-error":
					engine.err = errors.New("cannot start provider")
					expectedReason, expectedText, expectedTokens = "error", "", 0
				case "execution-error":
					main.deltas = append(main.deltas, contracts.DeltaError{Error: map[string]any{"code": "stream_failed", "message": "provider execution failed"}})
					expectedReason = "error"
				case "cancel":
					main.deltas = append(main.deltas, contracts.DeltaRunCancel{RunID: req.RunID})
					expectedReason = "cancel"
				case "recovery":
					main.deltas = append([]contracts.AgentDelta{
						contracts.DeltaStageMarker{Stage: "planning"},
						contracts.DeltaLLMRequest{ChatID: req.ChatID},
						contracts.DeltaReasoning{Text: "discarded reasoning"},
						contracts.DeltaContent{Text: "discarded content"},
						contracts.DeltaModelTurnDiscard{Reason: "stream_interrupted", Retrying: true},
						contracts.DeltaLLMRequest{ChatID: req.ChatID},
					}, main.deltas...)
					main.deltas = append(main.deltas, contracts.DeltaModelTurnCommit{RunSeq: 1})
				case "subagent":
					main.deltas = append([]contracts.AgentDelta{contracts.DeltaInvokeSubAgents{MainToolID: "invoke", Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer", TaskText: "child task"}}}}, main.deltas...)
					engine.streams = append(engine.streams, &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "child answer"}}, finalText: "child answer"})
				case "team":
					req.AgentKey, req.TeamID = "", "research"
					main.deltas = append([]contracts.AgentDelta{contracts.DeltaTeamDispatch{MainToolID: "delegate", Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}}}}, main.deltas...)
					engine.streams = append(engine.streams, &stubOrchestratableStream{deltas: []contracts.AgentDelta{
						contracts.DeltaContent{Text: "child answer"},
						contracts.DeltaUsageSnapshot{RunPromptTokens: 5, RunCompletionTokens: 3, RunTotalTokens: 8},
					}, finalText: "child answer"})
					expectedTokens = 18
				}
				fixture.server.deps.Agent = engine
				var completion *chat.RunCompletion
				switch entry {
				case "runtime":
					var start chat.RunStart
					result, err := fixture.server.ExecuteQuery(context.Background(), queryCommandFromAPI(req), runtimetypes.QueryHooks{OnRunStarted: func(value chat.RunStart) { start = value }})
					if err != nil {
						t.Fatal(err)
					}
					completion = result.Completion
					if result.Content != expectedText {
						t.Fatalf("runtime content=%q", result.Content)
					}
					if outcome == "recovery" && strings.Contains(result.FullText, "discarded") {
						t.Fatalf("runtime fullText retained discarded attempt: %s", result.FullText)
					}
					if completion == nil || start.StartedAtMillis != completion.StartedAtMillis {
						t.Fatalf("start/completion mismatch: %#v / %#v", start, completion)
					}
					if expectedReason == "error" && result.ErrorMessage == "" {
						t.Fatal("runtime lost execution error")
					}
				case "callback":
					var events []map[string]any
					err := fixture.server.ExecuteInternalQueryStream(context.Background(), req, func(data []byte) error {
						var event map[string]any
						if err := json.Unmarshal(data, &event); err != nil {
							return err
						}
						events = append(events, event)
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					terminal := findSSEMessageByType(t, events, "run."+expectedReason)
					if terminal["runId"] != req.RunID {
						t.Fatalf("callback terminal=%#v", terminal)
					}
				case "legacy":
					result, err := fixture.server.ExecuteInternalQueryResult(context.Background(), req, InternalQueryHooks{})
					if err != nil || result.StatusCode != http.StatusOK {
						t.Fatalf("legacy result=%#v err=%v", result, err)
					}
					completion = result.Completion
					if !strings.Contains(result.Body, "data: [DONE]") {
						t.Fatalf("legacy SSE missing DONE: %s", result.Body)
					}
				default:
					if entry == "json" {
						value := false
						req.Stream = &value
					}
					body, _ := json.Marshal(req)
					response := httptest.NewRecorder()
					fixture.server.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(body)))
					expectedStatus := http.StatusOK
					if entry == "json" && expectedReason == "error" {
						expectedStatus = http.StatusInternalServerError
					}
					if response.Code != expectedStatus {
						t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
					}
					if entry == "json" && expectedReason != "error" {
						var result api.ApiResponse[api.QueryResponse]
						if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
							t.Fatal(err)
						}
						if result.Data.Content != expectedText {
							t.Fatalf("content=%q want %q", result.Data.Content, expectedText)
						}
						if outcome == "recovery" && result.Data.FullText != nil && strings.Contains(*result.Data.FullText, "discarded") {
							t.Fatalf("JSON fullText retained discarded attempt: %s", *result.Data.FullText)
						}
						if result.Data.FullText == nil || !strings.Contains(*result.Data.FullText, expectedText) {
							t.Fatalf("fullText=%#v", result.Data.FullText)
						}
					}
				}
				runs, err := fixture.chats.ListRuns(req.ChatID)
				if err != nil || len(runs) != 1 {
					t.Fatalf("runs=%#v err=%v", runs, err)
				}
				if outcome == "recovery" {
					jsonl, err := fixture.chats.LoadJSONLContent(req.ChatID)
					if err != nil || !strings.Contains(jsonl, `"stage":"planning"`) {
						t.Fatalf("missing planning stage, err=%v", err)
					}
				}
				run := runs[0]
				if run.FinishReason != expectedReason || run.AssistantText != expectedText {
					t.Fatalf("persisted run=%#v", run)
				}
				if run.Usage.TotalTokens != expectedTokens {
					t.Fatalf("tokens=%d want %d", run.Usage.TotalTokens, expectedTokens)
				}

				if completion != nil && (completion.FinishReason != run.FinishReason || completion.AssistantText != run.AssistantText || completion.UpdatedAtMillis != run.CompletedAt) {
					t.Fatalf("returned completion differs from stored run: %#v / %#v", completion, run)
				}
				if (outcome == "team" || outcome == "subagent") && (len(main.injected) != 1 || !strings.Contains(main.injected[0].text, "child answer")) {
					t.Fatalf("child dispatch was skipped: %#v", main.injected)
				}
			})
		}
	}
}
