package server

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
)

func TestCoderPlanningSteerReplacesProposal(t *testing.T) {
	for _, scenario := range []struct {
		name                                   string
		waiting, rejected, repeat, stop, image bool
	}{
		{name: "during_output"},
		{name: "while_confirming", waiting: true},
		{name: "after_rejection", rejected: true},
		{name: "repeated_replanning", repeat: true},
		{name: "steer_cancels_planning", waiting: true, stop: true},
		{name: "frozen_image_during_output", image: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var imageData bytes.Buffer
			if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
				t.Fatal(err)
			}
			frozenImageURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageData.Bytes())
			targetRevision := 1
			if scenario.rejected {
				targetRevision = 2
			}
			lastRevision := targetRevision + 1
			if scenario.repeat {
				lastRevision++
			}
			var calls atomic.Int32
			gate := make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				call := int(calls.Add(1))
				if call > targetRevision {
					body := string(mustJSONMarshal(t, payload))
					if !strings.Contains(body, "STEER_REQUIREMENT_A") || !strings.Contains(body, "STEER_REQUIREMENT_B") {
						t.Errorf("new model request lost steer: %s", body)
					}
					if !strings.Contains(body, contracts.PlanningSuperseded) {
						t.Error("obsolete tool result absent from model history")
					}
					if scenario.image && !strings.Contains(body, frozenImageURL) {
						t.Error("frozen image absent from next model input")
					}
					if scenario.repeat && call > targetRevision+1 && !strings.Contains(body, "STEER_REQUIREMENT_C") {
						t.Error("later steer absent from revised model input")
					}
					assertBalancedPlanningMessages(t, normalizeProviderMessages(payload["messages"]))
				}
				if scenario.stop && call == targetRevision+1 {
					writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"stopped as requested"},"finish_reason":"stop"}]}`, `[DONE]`)
					return
				}
				if call <= lastRevision {
					assertCoderPlanningToolSet(t, providerRequestToolNames(payload["tools"]))
					if call == targetRevision && !scenario.waiting {
						writeProviderSSE(t, w, providerToolCallArgsDeltaFrame(t, fmt.Sprintf("tool_plan_%d", call), "finalize_planning", fmt.Sprintf(`{"markdown":"# Plan %d\n\nDraft`, call), ""))
						select {
						case <-gate:
						case <-r.Context().Done():
							return
						}
						writeProviderSSE(t, w, providerToolCallArgsDeltaFrame(t, "", "", ` completed."}`, "tool_calls"), `[DONE]`)
					} else {
						writeProviderSSE(t, w, providerToolCallFrame(t, fmt.Sprintf("tool_plan_%d", call), "finalize_planning", map[string]any{"markdown": fmt.Sprintf("# Plan %d\n\nComplete revised proposal.", call)}), `[DONE]`)
					}
					return
				}
				if call != lastRevision+1 {
					t.Errorf("unexpected model call %d", call)
				}
				assertStringSliceExcludes(t, providerRequestToolNames(payload["tools"]), "finalize_planning", "ask_user_question")
				assertProviderMessagesContainToolResult(t, payload, fmt.Sprintf("tool_plan_%d", lastRevision), "finalize_planning", "approve")
				writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"executed revised plan"},"finish_reason":"stop"}]}`, `[DONE]`)
			}, testFixtureOptions{setupRuntime: func(root string, cfg *config.Config) {
				if scenario.image {
					modelFile := filepath.Join(root, "registries", "models", "mock-model.yml")
					data, err := os.ReadFile(modelFile)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(modelFile, append(data, []byte("\nisVision: true\n")...), 0600); err != nil {
						t.Fatal(err)
					}
				}
				workspace := filepath.Join(root, "workspace")
				agentDir := filepath.Join(cfg.Paths.AgentsDir, "coder-app")
				for _, dir := range []string{workspace, agentDir} {
					if err := os.MkdirAll(dir, 0755); err != nil {
						t.Fatal(err)
					}
				}
				definition := "key: coder-app\nname: Coder App\nmode: CODER\nmodelConfig:\n  modelKey: mock-model\nruntimeConfig:\n  workspaceRoot: " + filepath.ToSlash(workspace) + "\n"
				if err := os.WriteFile(filepath.Join(agentDir, "agent.yml"), []byte(definition), 0644); err != nil {
					t.Fatal(err)
				}
			}})
			server := newLoopbackServer(t, fixture.server)
			defer server.Close()
			defer release()
			client := http.Client{Timeout: 10 * time.Second}
			const chatID = "chat-steered-planning"
			resp, err := client.Post(server.URL+"/api/query", "application/json", strings.NewReader(`{"chatId":"`+chatID+`","agentKey":"coder-app","message":"create a plan","planningMode":true}`))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			reader := bufio.NewReader(resp.Body)
			var body strings.Builder
			runID := ""
			if scenario.rejected {
				var awaiting string
				runID, awaiting = readAwaitingApproval(t, reader, &body, "confirm")
				submitFrontendDecisionWithReason(t, fixture.server, runID, awaiting, "reject", "revise the first plan")
			}
			if scenario.waiting {
				runID, _ = readAwaitingApproval(t, reader, &body, "confirm")
			} else {
				for {
					line, err := readSSELineWithTimeout(t, reader, "planning.delta")
					body.WriteString(line)
					if strings.HasPrefix(line, "data: {") {
						event := decodeSSELine(t, line)
						if id := stringValue(event["runId"]); id != "" {
							runID = id
						}
						if event["type"] == "planning.delta" {
							break
						}
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			sendSteer := func(message string) {
				t.Helper()
				rec := httptest.NewRecorder()
				payload := map[string]any{"agentKey": "coder-app", "runId": runID, "chatId": chatID, "message": message}
				if scenario.image && strings.Contains(message, "STEER_REQUIREMENT_A") {
					if err := os.WriteFile(filepath.Join(fixture.chats.ChatDir(chatID), "steer.png"), imageData.Bytes(), 0600); err != nil {
						t.Fatal(err)
					}
					payload["references"] = []api.Reference{{URL: "steer.png"}}
				}
				req := httptest.NewRequest(http.MethodPost, "/api/steer", bytes.NewReader(mustJSONMarshal(t, payload)))
				req.Header.Set("Content-Type", "application/json")
				fixture.server.ServeHTTP(rec, req)
				var result api.ApiResponse[api.SteerResponse]
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if rec.Code != 200 || !result.Data.Accepted {
					t.Fatalf("steer: %s", rec.Body.String())
				}
				if scenario.image {
					_ = os.Remove(filepath.Join(fixture.chats.ChatDir(chatID), "steer.png"))
				}
			}
			if scenario.waiting {
				message := "STEER_REQUIREMENT_A and STEER_REQUIREMENT_B: replace the plan and keep the tests"
				if scenario.stop {
					message = "Cancel planning, including STEER_REQUIREMENT_A and STEER_REQUIREMENT_B."
				}
				sendSteer(message)
			} else {
				sendSteer("STEER_REQUIREMENT_A: replace the plan")
				sendSteer("STEER_REQUIREMENT_B: keep the tests")
			}
			release()
			if !scenario.stop {
				_, awaiting := readAwaitingApproval(t, reader, &body, "confirm")
				if awaiting != fmt.Sprintf("tool_plan_%d", targetRevision+1) {
					t.Fatalf("obsolete plan requested confirmation: %s", awaiting)
				}
				if scenario.repeat {
					sendSteer("STEER_REQUIREMENT_C: revise once more")
					_, awaiting = readAwaitingApproval(t, reader, &body, "confirm")
					if awaiting != fmt.Sprintf("tool_plan_%d", lastRevision) {
						t.Fatalf("wrong final revision: %s", awaiting)
					}
				}
				stale := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewReader(mustJSONMarshal(t, map[string]any{
					"agentKey": "coder-app", "chatId": chatID, "runId": runID, "awaitingId": fmt.Sprintf("tool_plan_%d", targetRevision),
					"submitId": "stale-approve", "params": []any{map[string]any{"id": "confirm", "decision": "approve"}},
				})))
				request.Header.Set("Content-Type", "application/json")
				fixture.server.ServeHTTP(stale, request)
				if stale.Code != 409 {
					t.Fatalf("obsolete approval was not rejected: %d %s", stale.Code, stale.Body.String())
				}
				submitFrontendDecision(t, fixture.server, runID, awaiting, "approve")
			}
			readRemainingSSEUntilEOF(t, reader, &body)
			if !scenario.stop {
				waitForProviderCallCount(t, &calls, int32(lastRevision+1))
				executionID := waitForJSONLCoderExecuteRunID(t, fixture.chats, chatID, runID)
				execution := attachRunSSE(t, server.URL, "coder-app", executionID)
				if !strings.Contains(execution, "executed revised plan") {
					t.Fatal(execution)
				}
			}
			live := decodeSSEMessages(t, body.String())
			steers, invalidations, staleAsks, staleAnswers := 0, 0, 0, 0
			for _, event := range live {
				switch event["type"] {
				case "request.steer":
					steers++
				case "planning.superseded":
					invalidations++
				case "awaiting.ask":
					if event["awaitingId"] == fmt.Sprintf("tool_plan_%d", targetRevision) {
						staleAsks++
					}
				case "awaiting.answer":
					if event["awaitingId"] == fmt.Sprintf("tool_plan_%d", targetRevision) {
						staleAnswers++
					}
				case "request.submit":
					if event["awaitingId"] == fmt.Sprintf("tool_plan_%d", targetRevision) {
						t.Fatal("steer invented a submit")
					}
				}
			}
			wantSteers, wantInvalidations := 2, 1
			if scenario.waiting {
				wantSteers = 1
			}
			if scenario.repeat {
				wantSteers++
				wantInvalidations++
			}
			if steers != wantSteers || invalidations != wantInvalidations {
				t.Fatalf("steers=%d superseded=%d", steers, invalidations)
			}
			wantOldAwaiting := 0
			if scenario.waiting {
				wantOldAwaiting = 1
			}
			if staleAsks != wantOldAwaiting || staleAnswers != wantOldAwaiting {
				t.Fatalf("old asks=%d answers=%d", staleAsks, staleAnswers)
			}
			detail, err := fixture.chats.LoadChat(chatID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.stop && detail.Planning != nil {
				t.Fatalf("obsolete plan still current: %#v", detail.Planning)
			}
			if !scenario.stop && (detail.Planning == nil || detail.Planning.PlanningID != fmt.Sprintf("%s_planning_%d", runID, lastRevision)) {
				t.Fatalf("wrong replay plan: %#v", detail.Planning)
			}
			replayedSteers, replayedInvalidations := 0, 0
			for _, event := range detail.Events {
				if event.Type == "request.steer" {
					replayedSteers++
				}
				if event.Type == "planning.superseded" {
					replayedInvalidations++
				}
			}
			if replayedSteers != wantSteers || replayedInvalidations != wantInvalidations {
				t.Fatalf("replay lost steer/invalidation: %d/%d", replayedSteers, replayedInvalidations)
			}
			assertBalancedPlanningMessages(t, detail.RawMessages)
			if scenario.image && !strings.Contains(string(mustJSONMarshal(t, detail.RawMessages)), frozenImageURL) {
				t.Fatal("image steer snapshot missing from raw history")
			}
			pending, err := fixture.chats.LoadAllPendingAwaitings()
			if err != nil || len(pending) != 0 {
				t.Fatalf("dangling awaitings: %#v %v", pending, err)
			}
		})
	}
}

func assertBalancedPlanningMessages(t *testing.T, messages []map[string]any) {
	t.Helper()
	pending := map[string]bool{}
	for _, message := range messages {
		if stringValue(message["role"]) == "assistant" {
			for _, raw := range anySliceForServerTest(message["tool_calls"]) {
				call, _ := raw.(map[string]any)
				function, _ := call["function"].(map[string]any)
				if function["name"] == "finalize_planning" {
					pending[stringValue(call["id"])] = true
				}
			}
		}
		if stringValue(message["role"]) == "tool" && strings.HasPrefix(stringValue(message["tool_call_id"]), "tool_plan_") {
			id := stringValue(message["tool_call_id"])
			if !pending[id] {
				t.Fatalf("duplicate/orphan planning result: %s", id)
			}
			delete(pending, id)
		}
		if stringValue(message["role"]) == "user" && len(pending) > 0 {
			t.Fatalf("user input before planning result: %#v", pending)
		}
	}
	if len(pending) > 0 {
		t.Fatalf("unfinished planning tool calls: %#v", pending)
	}
}
