package server

import (
	"bufio"
	"bytes"
	"context"
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

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
)

func TestInteractionSubmitAndSteerAreConsumedBeforeNextTurn(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Need confirmation\",\"type\":\"select\",\"options\":[{\"label\":\"Approve\",\"description\":\"Continue with the request\",\"previewHtml\":\"<div><strong>Approve</strong></div>\"}],\"allowFreeText\":false}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"final answer"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query?detail=full", "application/json", bytes.NewBufferString(`{"message":"please confirm first"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	awaitingID := ""
	agentKey := ""
	var awaitQuestionPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "awaiting.ask" {
				awaitQuestionPayload = payload
				runID, _ = payload["runId"].(string)
				awaitingID, _ = payload["awaitingId"].(string)
				agentKey, _ = payload["agentKey"].(string)
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}
	if awaitQuestionPayload == nil {
		t.Fatalf("expected awaiting.ask before submit, got %s", streamBody.String())
	}
	if strings.Contains(streamBody.String(), `"type":"tool.start"`) {
		t.Fatalf("did not expect hidden ask_user_question tool events, got %s", streamBody.String())
	}
	if awaitingID == "" {
		t.Fatalf("expected awaitingId on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if agentKey != "mock-agent" {
		t.Fatalf("expected agentKey on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["view"].(map[string]any)["renderer"] != "native" || awaitQuestionPayload["view"].(map[string]any)["key"] != "question" {
		t.Fatalf("expected builtin question viewport on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["toolTimeout"]; exists {
		t.Fatalf("did not expect toolTimeout on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["timeout"] != float64(210) {
		t.Fatalf("expected await question timeout 210, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["mode"] != "question" {
		t.Fatalf("expected await question mode question, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["awaitName"]; exists {
		t.Fatalf("did not expect awaitName on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["chatId"]; exists {
		t.Fatalf("did not expect chatId on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	questions, _ := awaitQuestionPayload["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("expected question awaiting.ask questions length 1, got %#v", awaitQuestionPayload)
	}
	firstQuestion, _ := questions[0].(map[string]any)
	if firstQuestion["id"] != "q1" || firstQuestion["question"] != "Need confirmation" {
		t.Fatalf("unexpected inline question payload %#v", firstQuestion)
	}
	questionOptions, _ := firstQuestion["options"].([]any)
	if len(questionOptions) != 1 {
		t.Fatalf("expected one question option, got %#v", firstQuestion)
	}
	firstOption, _ := questionOptions[0].(map[string]any)
	if firstOption["description"] != "Continue with the request" || firstOption["previewHtml"] != "<div><strong>Approve</strong></div>" {
		t.Fatalf("expected previewHtml and description to be preserved on option, got %#v", firstOption)
	}

	steerReq := httptest.NewRequest(http.MethodPost, "/api/steer", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"`+runID+`","message":"Please keep it short."}`))
	steerReq.Header.Set("Content-Type", "application/json")
	steerRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(steerRec, steerReq)
	if steerRec.Code != http.StatusOK {
		t.Fatalf("steer expected 200, got %d: %s", steerRec.Code, steerRec.Body.String())
	}
	var steerResp api.ApiResponse[api.SteerResponse]
	if err := json.Unmarshal(steerRec.Body.Bytes(), &steerResp); err != nil {
		t.Fatalf("decode steer response: %v", err)
	}
	if !steerResp.Data.Accepted || steerResp.Data.Status != "accepted" {
		t.Fatalf("expected accepted steer, got %#v", steerResp.Data)
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"`+agentKey+`","runId":"`+runID+`","awaitingId":"`+awaitingID+`","params":[{"id":"q1","answer":"Approve"}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}
	var submitResp api.ApiResponse[api.SubmitResponse]
	if err := json.Unmarshal(submitRec.Body.Bytes(), &submitResp); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if !submitResp.Data.Accepted || submitResp.Data.Status != "accepted" {
		t.Fatalf("expected accepted submit, got %#v", submitResp.Data)
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("expected awaiting.ask event, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.payload"`) {
		t.Fatalf("did not expect awaiting.payload event for question mode, got %s", body)
	}
	if !strings.Contains(body, `"questions":[`) {
		t.Fatalf("expected top-level questions in question awaiting.ask event, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) {
		t.Fatalf("expected request.submit event, got %s", body)
	}
	if !strings.Contains(body, `"params":[{"id":"q1","answer":"Approve"}]`) {
		t.Fatalf("expected request.submit params, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) {
		t.Fatalf("expected awaiting.answer event, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.steer"`) {
		t.Fatalf("expected request.steer event, got %s", body)
	}
	if !strings.Contains(body, `"mode":"question"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"answers":[{"answer":"Approve","id":"q1","question":"Need confirmation"}]`) {
		t.Fatalf("expected normalized question awaiting.answer, got %s", body)
	}
	if !strings.Contains(body, "final answer") {
		t.Fatalf("expected final answer in stream, got %s", body)
	}
	assertEventOrder(t, body, "awaiting.ask", "request.submit", "awaiting.answer", "request.steer")

	chatsRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected 1 chat, got %d", len(chatsResp.Data))
	}

	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?detail=full&chatId="+chatsResp.Data[0].ChatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	foundAwaitAsk := false
	foundRequestSubmit := false
	foundAwaitingAnswer := false
	foundQuestionSnapshot := false
	for _, event := range chatResp.Data.Events {
		switch event.Type {
		case "tool.snapshot":
			if event.String("toolName") != "ask_user_question" {
				continue
			}
			foundQuestionSnapshot = true
		case "awaiting.ask":
			foundAwaitAsk = true
			if event.String("mode") != "question" || event.Value("view").(map[string]any)["renderer"] != "native" || event.Value("view").(map[string]any)["key"] != "question" {
				t.Fatalf("unexpected awaiting.ask payload %#v", event)
			}
			if _, exists := event.Payload["awaitName"]; exists {
				t.Fatalf("did not expect awaitName on awaiting.ask in chat detail, got %#v", event)
			}
			if _, exists := event.Payload["chatId"]; exists {
				t.Fatalf("did not expect chatId on awaiting.ask in chat detail, got %#v", event)
			}
			questions, _ := event.Payload["questions"].([]any)
			if len(questions) != 1 {
				t.Fatalf("expected question awaiting.ask questions length 1, got %#v", event)
			}
		case "request.submit":
			foundRequestSubmit = true
			if event.Value("params") == nil {
				t.Fatalf("expected params on request.submit in chat detail, got %#v", event)
			}
		case "awaiting.answer":
			foundAwaitingAnswer = true
			answers, _ := event.Value("answers").([]any)
			if event.String("mode") != "question" || event.String("status") != "answered" || len(answers) != 1 {
				t.Fatalf("unexpected awaiting.answer in chat detail %#v", event)
			}
		}
	}
	if !foundAwaitAsk {
		t.Fatalf("expected awaiting.ask in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundQuestionSnapshot {
		t.Fatalf("expected ask_user_question tool snapshot in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundRequestSubmit {
		t.Fatalf("expected request.submit in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundAwaitingAnswer {
		t.Fatalf("expected awaiting.answer in chat detail, got %#v", chatResp.Data.Events)
	}
	jsonlContent, err := fixture.chats.LoadJSONLContent(chatsResp.Data[0].ChatID)
	if err != nil {
		t.Fatalf("load chat jsonl: %v", err)
	}
	if strings.Contains(jsonlContent, `"inputMessages"`) {
		t.Fatalf("did not expect steer to persist as step inputMessages:\n%s", jsonlContent)
	}
	if !strings.Contains(jsonlContent, `"_type":"steer"`) {
		t.Fatalf("expected persisted steer event line in:\n%s", jsonlContent)
	}

	select {
	case messages := <-secondTurnMessages:
		toolIndex := -1
		steerIndex := -1
		for i, message := range messages {
			role, _ := message["role"].(string)
			content, _ := message["content"].(string)
			if role == "tool" {
				toolIndex = i
			}
			if role == "user" && content == "Please keep it short." {
				steerIndex = i
			}
		}
		if toolIndex < 0 {
			t.Fatalf("expected second turn to include tool message, got %#v", messages)
		}
		if steerIndex <= toolIndex {
			t.Fatalf("expected steer message after tool message, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

func TestSubAgentAwaitingSubmitRoutesPublicAwaitingID(t *testing.T) {
	runSubAgentAwaitingSubmitRoutingTest(t, false)
}

func TestSubAgentAwaitingSubmitAcceptsTaskRunIDCompatibility(t *testing.T) {
	runSubAgentAwaitingSubmitRoutingTest(t, true)
}

func runSubAgentAwaitingSubmitRoutingTest(t *testing.T, useTaskRunID bool) {
	t.Helper()
	var providerCallCount atomic.Int32
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_main_invoke","type":"function","function":{"name":"agent_invoke","arguments":"{\"tasks\":[{\"subAgentKey\":\"child-agent\",\"task\":\"ask the user to choose\",\"taskName\":\"Child Ask\"}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"raw_child_await","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Pick route\",\"type\":\"select\",\"options\":[{\"label\":\"Approve\",\"description\":\"Continue\"}]}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 3:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"child accepted"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		case 4:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"main summarized"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			mainDir := filepath.Join(cfg.Paths.AgentsDir, "main-agent")
			childDir := filepath.Join(cfg.Paths.AgentsDir, "child-agent")
			if err := os.MkdirAll(mainDir, 0o755); err != nil {
				t.Fatalf("mkdir main agent: %v", err)
			}
			if err := os.MkdirAll(childDir, 0o755); err != nil {
				t.Fatalf("mkdir child agent: %v", err)
			}
			writeAgentConfig(t, filepath.Join(mainDir, "agent.yml"), []string{
				"key: main-agent",
				"name: Main Agent",
				"role: orchestrator",
				"description: test orchestrator",
				"modelConfig:",
				"  modelKey: mock-model",
				"toolConfig:",
				"  tools:",
				"    - agent_invoke",
				"mode: GENERAL",
				"react:",
				"  maxSteps: 6",
			})
			writeAgentConfig(t, filepath.Join(childDir, "agent.yml"), []string{
				"key: child-agent",
				"name: Child Agent",
				"role: child",
				"description: test child",
				"visibility:",
				"  scopes:",
				"    - invoke",
				"modelConfig:",
				"  modelKey: mock-model",
				"toolConfig:",
				"  tools:",
				"    - ask_user_question",
				"mode: GENERAL",
				"react:",
				"  maxSteps: 6",
				"budget:",
				"  hitl:",
				"    timeout: 60",
			})
		},
	})
	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	chatID := "chat-subagent-hitl"
	resp, err := http.Post(httpServer.URL+"/api/query?detail=full", "application/json", bytes.NewBufferString(`{"chatId":"`+chatID+`","agentKey":"main-agent","message":"delegate"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	var runID string
	var taskID string
	var awaitingID string
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "awaiting.ask" {
				runID, _ = payload["runId"].(string)
				taskID, _ = payload["taskId"].(string)
				awaitingID, _ = payload["awaitingId"].(string)
				if runID != "" || taskID != "" || awaitingID != "" {
					break
				}
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before child awaiting: %v\n%s", readErr, streamBody.String())
		}
	}
	if runID == "" || taskID == "" || awaitingID == "" {
		t.Fatalf("expected child awaiting identifiers, got run=%q task=%q awaiting=%q stream=%s", runID, taskID, awaitingID, streamBody.String())
	}
	if taskID != runID+"_t_1" {
		t.Fatalf("expected taskId to derive from parent run, run=%q task=%q", runID, taskID)
	}
	if awaitingID != taskID+":raw_child_await" {
		t.Fatalf("expected namespaced awaiting id, got %q", awaitingID)
	}

	submitRunID := runID
	if useTaskRunID {
		submitRunID = taskID
	}
	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"chatId":"`+chatID+`","agentKey":"main-agent","runId":"`+submitRunID+`","awaitingId":"`+awaitingID+`","submitId":"submit_child_1","params":[{"id":"q1","answer":"Approve"}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s\nstream=%s", submitRec.Code, submitRec.Body.String(), streamBody.String())
	}
	var submitResp api.ApiResponse[api.SubmitResponse]
	if err := json.Unmarshal(submitRec.Body.Bytes(), &submitResp); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	if !submitResp.Data.Accepted || submitResp.Data.Status != "accepted" || submitResp.Data.AwaitingID != awaitingID || submitResp.Data.RunID != runID {
		t.Fatalf("unexpected submit response %#v", submitResp.Data)
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}
	body := streamBody.String()
	if !strings.Contains(body, `"type":"request.submit"`) || !strings.Contains(body, `"taskId":"`+taskID+`"`) || !strings.Contains(body, `"awaitingId":"`+awaitingID+`"`) {
		t.Fatalf("expected public child request.submit, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) || !strings.Contains(body, `"taskId":"`+taskID+`"`) || !strings.Contains(body, `"awaitingId":"`+awaitingID+`"`) {
		t.Fatalf("expected public child awaiting.answer, got %s", body)
	}
	if !strings.Contains(body, "child accepted") || !strings.Contains(body, "main summarized") {
		t.Fatalf("expected child and main final content, got %s", body)
	}
}

func TestAwaitingChatDetailExposesReplayUsageWithoutUsageSnapshotEvent(t *testing.T) {
	var providerCallCount atomic.Int32

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Need confirmation\",\"type\":\"text\"}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":2822,"completion_tokens":100,"total_tokens":2922,"completion_tokens_details":{"reasoning_tokens":44}}}`,
				`[DONE]`,
			)
		case 2:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"confirmed"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query?detail=full", "application/json", bytes.NewBufferString(`{"message":"please ask first","agentKey":"mock-agent"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	chatID := ""
	awaitingID := ""
	agentKey := ""
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if value, _ := payload["chatId"].(string); value != "" {
				chatID = value
			}
			if payload["type"] == "awaiting.ask" {
				runID, _ = payload["runId"].(string)
				awaitingID, _ = payload["awaitingId"].(string)
				agentKey, _ = payload["agentKey"].(string)
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before awaiting.ask: %v", readErr)
		}
	}
	if runID == "" || chatID == "" || awaitingID == "" || agentKey == "" {
		t.Fatalf("expected active awaiting identifiers, runID=%q chatID=%q awaitingID=%q agentKey=%q stream=%s", runID, chatID, awaitingID, agentKey, streamBody.String())
	}

	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?detail=full&chatId="+chatID, nil))
	if chatRec.Code != http.StatusOK {
		t.Fatalf("chat detail expected 200, got %d: %s", chatRec.Code, chatRec.Body.String())
	}
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	if chatResp.Data.Awaiting == nil ||
		chatResp.Data.Awaiting.AwaitingID != awaitingID ||
		chatResp.Data.Awaiting.RunID != runID ||
		chatResp.Data.Awaiting.Mode != "question" ||
		chatResp.Data.Awaiting.Status != "awaiting" ||
		chatResp.Data.Awaiting.CreatedAt <= 0 {
		t.Fatalf("expected active awaiting in chat detail, got %#v", chatResp.Data.Awaiting)
	}
	if chatResp.Data.ActiveRun == nil || chatResp.Data.ActiveRun.RunID != runID {
		t.Fatalf("expected active run %q in chat detail, got %#v", runID, chatResp.Data.ActiveRun)
	}

	toolIndex := -1
	awaitIndex := -1
	for i, event := range chatResp.Data.Events {
		switch event.Type {
		case "usage.snapshot":
			t.Fatalf("did not expect historical usage.snapshot event, got %#v", chatResp.Data.Events)
		case "tool.snapshot":
			if event.String("toolName") == "ask_user_question" {
				toolIndex = i
			}
		case "awaiting.ask":
			if event.String("awaitingId") == awaitingID {
				awaitIndex = i
			}
		}
	}
	if toolIndex < 0 || awaitIndex <= toolIndex {
		t.Fatalf("expected tool.snapshot before awaiting.ask, toolIndex=%d awaitIndex=%d events=%#v", toolIndex, awaitIndex, chatResp.Data.Events)
	}
	if chatResp.Data.Usage == nil || chatResp.Data.Usage.LastRun == nil || chatResp.Data.Usage.Chat == nil {
		t.Fatalf("expected replay usage while awaiting, got %#v", chatResp.Data.Usage)
	}
	if chatResp.Data.Usage.LastRun.PromptTokens != 2822 ||
		chatResp.Data.Usage.LastRun.CompletionTokens != 100 ||
		chatResp.Data.Usage.LastRun.TotalTokens != 2922 ||
		chatResp.Data.Usage.LastRun.LlmChatCompletionCount != 1 ||
		chatResp.Data.Usage.LastRun.CompletionTokensDetails == nil ||
		chatResp.Data.Usage.LastRun.CompletionTokensDetails.ReasoningTokens != 44 {
		t.Fatalf("unexpected last run replay usage %#v", chatResp.Data.Usage.LastRun)
	}
	if chatResp.Data.Usage.Chat.PromptTokens != 2822 ||
		chatResp.Data.Usage.Chat.CompletionTokens != 100 ||
		chatResp.Data.Usage.Chat.TotalTokens != 2922 ||
		chatResp.Data.Usage.Chat.LlmChatCompletionCount != 1 {
		t.Fatalf("unexpected chat replay usage %#v", chatResp.Data.Usage.Chat)
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"`+agentKey+`","runId":"`+runID+`","awaitingId":"`+awaitingID+`","params":[{"id":"q1","answer":"Approve"}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}
	for {
		if _, readErr := reader.ReadString('\n'); readErr == io.EOF {
			break
		} else if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}
}

func TestQuestionAwaitFollowsToolEnd(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Notification topics\",\"type\":\"multi-select\",\"options\":[{\"label\":\"产品更新\",\"description\":\"Release notes and new features\"},{\"label\":\"使用教程\",\"description\":\"How-to guides and walkthroughs\"}],\"allowFreeText\":false},{\"question\":\"How many people?\",\"type\":\"number\"}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"question flow complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query?detail=full", "application/json", bytes.NewBufferString(`{"message":"ask me a few things"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	awaitingID := ""
	var awaitQuestionPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "awaiting.ask":
				awaitQuestionPayload = payload
				runID, _ = payload["runId"].(string)
				awaitingID, _ = payload["awaitingId"].(string)
				goto questionSubmit
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

questionSubmit:
	if awaitQuestionPayload == nil {
		t.Fatalf("expected awaiting.ask for hidden ask_user_question, got %s", streamBody.String())
	}
	if awaitingID == "" {
		t.Fatalf("expected awaitingId on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["mode"] != "question" {
		t.Fatalf("expected question mode, got %#v", awaitQuestionPayload)
	}
	questions, _ := awaitQuestionPayload["questions"].([]any)
	if len(questions) != 2 {
		t.Fatalf("expected inline questions on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["awaitName"]; exists {
		t.Fatalf("did not expect awaitName on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["chatId"]; exists {
		t.Fatalf("did not expect chatId on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"`+runID+`","awaitingId":"`+awaitingID+`","params":[{"id":"q1","answers":["产品更新","使用教程"]},{"id":"q2","answer":2}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("expected awaiting.ask event, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.payload"`) {
		t.Fatalf("did not expect awaiting.payload event, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) {
		t.Fatalf("expected request.submit event, got %s", body)
	}
	if !strings.Contains(body, `"params":[{"id":"q1","answers":["产品更新","使用教程"]},{"id":"q2","answer":2}]`) {
		t.Fatalf("expected request.submit params array, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) {
		t.Fatalf("expected awaiting.answer event, got %s", body)
	}
	if !strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"answers":[{"answers":["产品更新","使用教程"],"id":"q1","question":"Notification topics"},{"answer":2,"id":"q2","question":"How many people?"}]`) {
		t.Fatalf("expected normalized awaiting.answer answers, got %s", body)
	}
	assertEventOrder(t, body, "awaiting.ask", "request.submit", "awaiting.answer")

	select {
	case messages := <-secondTurnMessages:
		toolContent := ""
		for _, message := range messages {
			if role, _ := message["role"].(string); role == "tool" {
				toolContent, _ = message["content"].(string)
				break
			}
		}
		if toolContent == "" {
			t.Fatalf("expected second turn to include tool message, got %#v", messages)
		}
		if toolContent != "问题：Notification topics\n回答：产品更新, 使用教程\n问题：How many people?\n回答：2" {
			t.Fatalf("expected qa-formatted tool content, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

func TestQuestionSubmitRejectsMultiSelectAnswerAndAllowsRetry(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnStarted := make(chan struct{}, 1)
	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch providerCallCount.Add(1) {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_lifestyle_question","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"你有哪些生活习惯？\",\"type\":\"multi-select\",\"options\":[{\"label\":\"A\",\"description\":\"早睡早起\"},{\"label\":\"B\",\"description\":\"规律运动\"}],\"allowFreeText\":false}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			secondTurnStarted <- struct{}{}
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"生活习惯已记录"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", providerCallCount.Load())
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query?detail=full", "application/json", bytes.NewBufferString(`{"message":"问我一个关于生活习惯方面的问题，类型为多选"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	var awaitQuestion map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "awaiting.ask" {
				awaitQuestion = payload
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

	if awaitQuestion == nil {
		t.Fatalf("expected question awaiting.ask, got %s", streamBody.String())
	}
	runID, _ := awaitQuestion["runId"].(string)
	awaitingID, _ := awaitQuestion["awaitingId"].(string)
	agentKey, _ := awaitQuestion["agentKey"].(string)
	questions, _ := awaitQuestion["questions"].([]any)
	if runID == "" || awaitingID == "" || agentKey == "" || len(questions) != 1 {
		t.Fatalf("unexpected awaiting question %#v", awaitQuestion)
	}
	question, _ := questions[0].(map[string]any)
	questionID, _ := question["id"].(string)
	if questionID == "" || question["type"] != "multi-select" {
		t.Fatalf("expected multi-select question with id, got %#v", question)
	}

	invalidParams, err := queryinput.EncodeSubmitParams([]map[string]any{{"id": questionID, "answer": "A"}})
	if err != nil {
		t.Fatalf("encode invalid submit params: %v", err)
	}
	invalidBody, err := json.Marshal(api.SubmitRequest{
		AgentKey:   agentKey,
		RunID:      runID,
		AwaitingID: awaitingID,
		Params:     invalidParams,
	})
	if err != nil {
		t.Fatalf("marshal invalid submit request: %v", err)
	}
	invalidRec := httptest.NewRecorder()
	invalidReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewReader(invalidBody))
	invalidReq.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusOK {
		t.Fatalf("invalid submit expected 200, got %d: %s", invalidRec.Code, invalidRec.Body.String())
	}
	var invalidResponse api.ApiResponse[api.SubmitResponse]
	if err := json.Unmarshal(invalidRec.Body.Bytes(), &invalidResponse); err != nil {
		t.Fatalf("decode invalid submit response: %v", err)
	}
	if invalidResponse.Code != 0 || invalidResponse.Data.Accepted || invalidResponse.Data.Status != "invalid" || !strings.Contains(invalidResponse.Data.Detail, "answers is required") {
		t.Fatalf("expected rejected multi-select submit, got %#v", invalidResponse)
	}
	if providerCallCount.Load() != 1 {
		t.Fatalf("invalid submit must not continue the model, got %d provider calls", providerCallCount.Load())
	}
	if _, ok := fixture.runs.LookupAwaiting(runID, awaitingID); !ok {
		t.Fatal("invalid submit must leave the awaiting item active")
	}

	validParams, err := queryinput.EncodeSubmitParams([]map[string]any{{"id": questionID, "answers": []string{"A"}}})
	if err != nil {
		t.Fatalf("encode valid submit params: %v", err)
	}
	validBody, err := json.Marshal(api.SubmitRequest{
		AgentKey:   agentKey,
		RunID:      runID,
		AwaitingID: awaitingID,
		Params:     validParams,
	})
	if err != nil {
		t.Fatalf("marshal valid submit request: %v", err)
	}
	validRec := httptest.NewRecorder()
	validReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewReader(validBody))
	validReq.Header.Set("Content-Type", "application/json")
	fixture.server.ServeHTTP(validRec, validReq)
	if validRec.Code != http.StatusOK {
		t.Fatalf("valid submit expected 200, got %d: %s", validRec.Code, validRec.Body.String())
	}
	var validResponse api.ApiResponse[api.SubmitResponse]
	if err := json.Unmarshal(validRec.Body.Bytes(), &validResponse); err != nil {
		t.Fatalf("decode valid submit response: %v", err)
	}
	if !validResponse.Data.Accepted || validResponse.Data.Status != "accepted" {
		t.Fatalf("expected accepted multi-select submit, got %#v", validResponse.Data)
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}
	select {
	case <-secondTurnStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("valid submit did not continue the model")
	}
	body := streamBody.String()
	if strings.Count(body, `"type":"request.submit"`) != 1 || strings.Count(body, `"type":"awaiting.answer"`) != 1 {
		t.Fatalf("invalid submit must not produce submit events, got %s", body)
	}
}

func TestQuestionChunkedArgsEmitAwaitAfterToolEnd(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Notification topics\",\"type\":\"multi-select\","}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"options\":[{\"label\":\"产品更新\",\"description\":\"Release notes and new features\"},{\"label\":\"使用教程\",\"description\":\"How-to guides and walkthroughs\"}],\"allowFreeText\":false},{\"question\":\"How many people?\",\"type\":\"number\"}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"chunked question flow complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query?detail=full", "application/json", bytes.NewBufferString(`{"message":"ask me a few things"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	awaitingID := ""
	var awaitQuestionPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "awaiting.ask":
				awaitQuestionPayload = payload
				runID, _ = payload["runId"].(string)
				awaitingID, _ = payload["awaitingId"].(string)
				goto chunkedQuestionSubmit
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

chunkedQuestionSubmit:
	if awaitQuestionPayload == nil {
		t.Fatalf("expected awaiting.ask after chunked tool args, got %s", streamBody.String())
	}
	if awaitingID == "" {
		t.Fatalf("expected awaitingId on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["mode"] != "question" {
		t.Fatalf("expected question mode, got %#v", awaitQuestionPayload)
	}
	questions, _ := awaitQuestionPayload["questions"].([]any)
	if len(questions) != 2 {
		t.Fatalf("expected inline questions on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"`+runID+`","awaitingId":"`+awaitingID+`","params":[{"id":"q1","answers":["产品更新","使用教程"]},{"id":"q2","answer":2}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("expected awaiting.ask event, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.payload"`) {
		t.Fatalf("did not expect awaiting.payload event, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) {
		t.Fatalf("expected request.submit event, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) {
		t.Fatalf("expected awaiting.answer event, got %s", body)
	}
	assertEventOrder(t, body, "awaiting.ask", "request.submit", "awaiting.answer")
	select {
	case messages := <-secondTurnMessages:
		toolContent := ""
		for _, message := range messages {
			if role, _ := message["role"].(string); role == "tool" {
				toolContent, _ = message["content"].(string)
				break
			}
		}
		if toolContent == "" {
			t.Fatalf("expected second turn to include tool message, got %#v", messages)
		}
		if toolContent != "问题：Notification topics\n回答：产品更新, 使用教程\n问题：How many people?\n回答：2" {
			t.Fatalf("expected qa-formatted tool content, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

func TestQuestionInvalidSelectOptionsFailsBeforeAwait(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Pick a plan\",\"type\":\"select\"}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"invalid question flow complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query?detail=full", "application/json", bytes.NewBufferString(`{"message":"ask me a question"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream: %v", readErr)
		}
	}

	body := streamBody.String()
	if strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("did not expect awaiting.ask for invalid question args, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.payload"`) {
		t.Fatalf("did not expect awaiting.payload for invalid question args, got %s", body)
	}
	select {
	case messages := <-secondTurnMessages:
		toolContent := ""
		for _, message := range messages {
			if role, _ := message["role"].(string); role == "tool" {
				toolContent, _ = message["content"].(string)
				break
			}
		}
		if !strings.Contains(toolContent, "invalid tool arguments: Pick a plan: options is required for select and multi-select questions") {
			t.Fatalf("expected invalid tool arguments in second turn tool message, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

func TestQuestionAwaitDismissReturnsCancelledStructuredResult(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Pick a plan\",\"type\":\"select\",\"options\":[{\"label\":\"Weekend\",\"description\":\"2 days\"}],\"allowFreeText\":false}]}"}}]},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"question cancel flow complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query?detail=full", "application/json", bytes.NewBufferString(`{"message":"ask me a question"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	toolID := ""
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "awaiting.ask" {
				runID, _ = payload["runId"].(string)
				toolID, _ = payload["awaitingId"].(string)
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"`+runID+`","awaitingId":"`+toolID+`","params":[]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	body := streamBody.String()
	if !strings.Contains(body, `"type":"request.submit"`) || !strings.Contains(body, `"params":[]`) {
		t.Fatalf("expected request.submit with empty params array, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, `"code":"user_dismissed"`) {
		t.Fatalf("expected dismissed awaiting.answer in stream, got %s", body)
	}
	assertEventOrder(t, body, "awaiting.ask", "request.submit", "awaiting.answer")

	select {
	case messages := <-secondTurnMessages:
		toolContent := ""
		for _, message := range messages {
			if role, _ := message["role"].(string); role == "tool" {
				toolContent, _ = message["content"].(string)
				break
			}
		}
		if toolContent == "" {
			t.Fatalf("expected second turn to include tool message, got %#v", messages)
		}
		if !strings.Contains(toolContent, `"status":"error"`) || !strings.Contains(toolContent, `"mode":"question"`) || !strings.Contains(toolContent, `"code":"user_dismissed"`) {
			t.Fatalf("expected dismissed JSON tool content, got %#v", messages)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}
}

type recordingSandbox struct {
	commands []string
	envs     []map[string]string
}

type scriptedSandbox struct {
	execute func(command string, cwd string, env map[string]string) contracts.SandboxExecutionResult
}

type stubMCPToolCatalog struct {
	defs []api.ToolDetailResponse
}

func (s *recordingSandbox) OpenIfNeeded(_ context.Context, _ *contracts.ExecutionContext) error {
	return nil
}

func (s *recordingSandbox) Execute(_ context.Context, _ *contracts.ExecutionContext, command string, cwd string, _ int64, env map[string]string) (contracts.SandboxExecutionResult, error) {
	if strings.HasPrefix(command, "test -d '") {
		target := strings.SplitN(strings.TrimPrefix(command, "test -d '"), "'", 2)[0]
		return contracts.SandboxExecutionResult{Stdout: target + "\n", Cwd: cwd}, nil
	}
	if strings.HasPrefix(command, "p=$(command -v") || strings.HasPrefix(command, "test -f ") {
		return contracts.SandboxExecutionResult{ExitCode: 127, Cwd: cwd}, nil
	}
	s.commands = append(s.commands, command)
	s.envs = append(s.envs, contracts.CloneStringMap(env))
	return contracts.SandboxExecutionResult{
		ExitCode: 0,
		Stdout:   "executed: " + command,
		Stderr:   "",
		Cwd:      cwd,
	}, nil
}

func (s *recordingSandbox) CloseQuietly(_ *contracts.ExecutionContext) {}

func (s *scriptedSandbox) OpenIfNeeded(_ context.Context, _ *contracts.ExecutionContext) error {
	return nil
}

func (s *scriptedSandbox) Execute(_ context.Context, _ *contracts.ExecutionContext, command string, cwd string, _ int64, env map[string]string) (contracts.SandboxExecutionResult, error) {
	if strings.HasPrefix(command, "test -d '") {
		target := strings.SplitN(strings.TrimPrefix(command, "test -d '"), "'", 2)[0]
		return contracts.SandboxExecutionResult{Stdout: target + "\n", Cwd: cwd}, nil
	}
	if strings.HasPrefix(command, "p=$(command -v -- 'ls')") || strings.HasPrefix(command, "p=$(command -v -- '/usr/bin/ls')") {
		return contracts.SandboxExecutionResult{Stdout: "/usr/bin/ls\n", Cwd: cwd}, nil
	}
	if s.execute == nil {
		return contracts.SandboxExecutionResult{ExitCode: 0, Cwd: cwd}, nil
	}
	return s.execute(command, cwd, env), nil
}

func (s *scriptedSandbox) CloseQuietly(_ *contracts.ExecutionContext) {}

func (c stubMCPToolCatalog) Definitions() []api.ToolDetailResponse {
	return append([]api.ToolDetailResponse(nil), c.defs...)
}

func (c stubMCPToolCatalog) Tool(name string) (api.ToolDetailResponse, bool) {
	for _, def := range c.defs {
		if strings.EqualFold(def.Name, name) || strings.EqualFold(def.Key, name) {
			return def, true
		}
	}
	return api.ToolDetailResponse{}, false
}

func (c stubMCPToolCatalog) ToolNamesForServers(serverKeys []string) []string {
	selected := make(map[string]struct{}, len(serverKeys))
	for _, serverKey := range serverKeys {
		if key := strings.ToLower(strings.TrimSpace(serverKey)); key != "" {
			selected[key] = struct{}{}
		}
	}
	result := make([]string, 0)
	for _, def := range c.defs {
		serverKey, _ := def.Meta["serverKey"].(string)
		if _, ok := selected[strings.ToLower(strings.TrimSpace(serverKey))]; ok {
			result = append(result, def.Name)
		}
	}
	return result
}

func TestBashHITLApproveFlowReplaysApprovalSummaryInChatRawMessages(t *testing.T) {
	var providerCallCount atomic.Int32
	command := "rm -r /workspace/old-images"
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_bash", "bash", map[string]any{
					"command":     command,
					"description": "执行测试命令",
					"cwd":         "/workspace",
				}),
				`[DONE]`,
			)
		case 2:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"bash hitl complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		sandbox: &recordingSandbox{},
		configure: func(cfg *config.Config) {
			cfg.Defaults.Budget.Hitl.Timeout = 600
		},
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query?detail=full", "application/json", bytes.NewBufferString(`{"message":"please push the change"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	awaitingID := ""
	approvalID := ""
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "awaiting.ask" {
				awaitingID, _ = payload["awaitingId"].(string)
				if approvals, ok := payload["approvals"].([]any); ok && len(approvals) > 0 {
					if firstApproval, ok := approvals[0].(map[string]any); ok {
						approvalID, _ = firstApproval["id"].(string)
					}
				}
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}
	if awaitingID == "" || approvalID == "" {
		t.Fatalf("expected approval awaiting payload, got %s", streamBody.String())
	}

	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"`+extractRunIDFromStream(t, streamBody.String())+`","awaitingId":"`+awaitingID+`","params":[{"id":"`+approvalID+`","decision":"approve"}]}`)))
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	for {
		_, readErr := reader.ReadString('\n')
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			t.Fatalf("read query stream after submit: %v", readErr)
		}
	}

	chatsRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatsRec, httptest.NewRequest(http.MethodGet, "/api/chats", nil))
	var chatsResp api.ApiResponse[[]api.ChatSummaryResponse]
	if err := json.Unmarshal(chatsRec.Body.Bytes(), &chatsResp); err != nil {
		t.Fatalf("decode chats response: %v", err)
	}
	if len(chatsResp.Data) != 1 {
		t.Fatalf("expected one chat, got %#v", chatsResp)
	}

	chatID := chatsResp.Data[0].ChatID
	chatRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?detail=full&chatId="+chatID+"&includeRawMessages=true", nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}

	hitlIndex := -1
	hitlCount := 0
	for i, message := range chatResp.Data.RawMessages {
		if message["role"] == "user" && strings.Contains(stringValue(message["content"]), "[System audit") {
			hitlIndex = i
			hitlCount++
		}
	}
	if hitlCount != 1 {
		t.Fatalf("expected exactly one replayed HITL summary raw message, got %#v", chatResp.Data.RawMessages)
	}
	toolIndex := -1
	for i, message := range chatResp.Data.RawMessages {
		if message["role"] == "tool" {
			toolIndex = i
		}
	}
	if toolIndex < 0 || toolIndex >= hitlIndex {
		t.Fatalf("expected HITL raw message to appear after tool result, got %#v", chatResp.Data.RawMessages)
	}
	if !strings.Contains(stringValue(chatResp.Data.RawMessages[hitlIndex]["content"]), `tool=bash command="rm -r /workspace/old-images" decision=approve`) {
		t.Fatalf("expected replayed HITL audit notice content, got %#v", chatResp.Data.RawMessages[hitlIndex])
	}
}

func TestSandboxBashResultShapeAcrossStreamBoundaries(t *testing.T) {
	t.Run("success uses plain stdout for sse and tool message", func(t *testing.T) {
		body, secondTurn := runSandboxBashQueryForResultShape(t, &scriptedSandbox{
			execute: func(command string, cwd string, _ map[string]string) contracts.SandboxExecutionResult {
				return contracts.SandboxExecutionResult{
					ExitCode: 0,
					Stdout:   "listed from " + cwd + ": " + command + "\n",
					Cwd:      cwd,
				}
			},
		})

		resultPayload := findToolResultPayload(t, body, "tool_bash")
		if got, ok := resultPayload["result"].(string); !ok || got != "listed from /workspace: ls sample\n" {
			t.Fatalf("expected string tool.result payload, got %#v", resultPayload["result"])
		}
		toolContent := findToolMessageContent(t, secondTurn, "bash")
		if toolContent != "listed from /workspace: ls sample\n" {
			t.Fatalf("expected plain stdout tool message, got %q", toolContent)
		}
	})

	t.Run("failure uses structured object for sse and json for tool message", func(t *testing.T) {
		body, secondTurn := runSandboxBashQueryForResultShape(t, &scriptedSandbox{
			execute: func(_ string, cwd string, _ map[string]string) contracts.SandboxExecutionResult {
				return contracts.SandboxExecutionResult{
					ExitCode: 2,
					Stdout:   "",
					Stderr:   "ls: sample: No such file or directory\n",
					Cwd:      cwd,
				}
			},
		})

		resultPayload := findToolResultPayload(t, body, "tool_bash")
		resultObject, ok := resultPayload["result"].(map[string]any)
		if !ok {
			t.Fatalf("expected object tool.result payload, got %#v", resultPayload["result"])
		}
		if resultObject["exitCode"] != float64(2) {
			t.Fatalf("expected exitCode=2, got %#v", resultObject)
		}
		if resultObject["stderr"] != "ls: sample: No such file or directory\n" {
			t.Fatalf("expected stderr in result payload, got %#v", resultObject)
		}
		toolContent := findToolMessageContent(t, secondTurn, "bash")
		if !strings.HasPrefix(toolContent, "{") || !strings.Contains(toolContent, `"exitCode":2`) || !strings.Contains(toolContent, `"stderr":"ls: sample: No such file or directory\n"`) {
			t.Fatalf("expected JSON tool message for failure, got %q", toolContent)
		}
	})

}

func runSandboxBashQueryForResultShape(t *testing.T, sandbox contracts.SandboxClient) (string, []map[string]any) {
	t.Helper()

	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_bash", "bash", map[string]any{
					"command":     "ls sample",
					"description": "列出 sample",
					"cwd":         "/workspace",
				}),
				`[DONE]`,
			)
		case 2:
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode second provider request: %v", err)
			}
			secondTurnMessages <- normalizeProviderMessages(payload["messages"])
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"content":"query complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		sandbox: sandbox,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/query?detail=full", bytes.NewBufferString(`{"message":"list sample","agentKey":"mock-agent"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	select {
	case messages := <-secondTurnMessages:
		return rec.Body.String(), messages
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for second provider request, body=%s", rec.Body.String())
	}
	return "", nil
}

func extractRunIDFromStream(t *testing.T, body string) string {
	t.Helper()
	for _, message := range decodeSSEMessages(t, body) {
		if runID, _ := message["runId"].(string); runID != "" {
			return runID
		}
	}
	t.Fatalf("expected runId in stream body: %s", body)
	return ""
}

func TestSubmitRejectsUnknownAwaiting(t *testing.T) {
	fixture := newTestFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"agentKey":"mock-agent","runId":"missing-run","awaitingId":"missing-awaiting","params":[{"id":"q1","answer":"ok"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown_awaiting") {
		t.Fatalf("expected unknown_awaiting error, got %s", rec.Body.String())
	}
}

func mustEncodeSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := queryinput.EncodeSubmitParams(value)
	if err != nil {
		t.Fatalf("encode submit params: %v", err)
	}
	return params
}

func TestValidateSubmitParamsAllowsOrderedItemsWithoutIDs(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		itemCount int
		params    api.SubmitParams
	}{
		{
			name:      "question",
			mode:      "question",
			itemCount: 2,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"answer": "Weekend"},
				{"answers": []string{"产品更新", "使用教程"}},
			}),
		},
		{
			name:      "approval",
			mode:      "approval",
			itemCount: 1,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"decision": "approve"},
			}),
		},
		{
			name:      "approval batch",
			mode:      "approval",
			itemCount: 3,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"decision": "approve"},
				{"decision": "approve_rule_run"},
				{"decision": "reject"},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSubmitParams(contracts.AwaitingSubmitContext{
				AwaitingID: "await_1",
				Mode:       tt.mode,
				ItemCount:  tt.itemCount,
			}, tt.params)
			if err != nil {
				t.Fatalf("validateSubmitParams returned error: %v", err)
			}
		})
	}
}

func TestValidateSubmitParamsIgnoresSubmittedIDsWhenCountMatches(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		itemCount int
		params    api.SubmitParams
	}{
		{
			name:      "question",
			mode:      "question",
			itemCount: 2,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "wrong-1", "answer": "Weekend"},
				{"id": "wrong-2", "answer": 2},
			}),
		},
		{
			name:      "approval",
			mode:      "approval",
			itemCount: 1,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "wrong-cmd", "decision": "approve"},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSubmitParams(contracts.AwaitingSubmitContext{
				AwaitingID: "await_1",
				Mode:       tt.mode,
				ItemCount:  tt.itemCount,
			}, tt.params)
			if err != nil {
				t.Fatalf("validateSubmitParams returned error: %v", err)
			}
		})
	}
}

func TestValidateSubmitParamsRejectsCountMismatch(t *testing.T) {
	err := validateSubmitParams(contracts.AwaitingSubmitContext{
		AwaitingID: "await_1",
		Mode:       "question",
		ItemCount:  2,
	}, mustEncodeSubmitParams(t, []map[string]any{
		{"answer": "Weekend"},
	}))
	if err == nil || !strings.Contains(err.Error(), "expected 2 submit items, got 1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSubmitParamsRejectsPlanCountMismatch(t *testing.T) {
	err := validateSubmitParams(contracts.AwaitingSubmitContext{
		AwaitingID: "await_1",
		Mode:       "plan",
		ItemCount:  1,
	}, mustEncodeSubmitParams(t, []map[string]any{
		{"decision": "approve"},
		{"decision": "reject"},
	}))
	if err == nil || !strings.Contains(err.Error(), "expected 1 submit items, got 2") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSubmitParamsRejectsInvalidShape(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		item       map[string]any
		wantSubstr string
	}{
		{
			name:       "question decision",
			mode:       "question",
			item:       map[string]any{"decision": "approve"},
			wantSubstr: "items[0]: question items require exactly one of answer or answers",
		},
		{
			name:       "approval missing decision",
			mode:       "approval",
			item:       map[string]any{"reason": "nope"},
			wantSubstr: "items[0]: approval items require decision",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSubmitParams(contracts.AwaitingSubmitContext{
				AwaitingID: "await_1",
				Mode:       tt.mode,
				ItemCount:  1,
			}, mustEncodeSubmitParams(t, []map[string]any{tt.item}))
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func assertEventOrder(t *testing.T, body string, eventTypes ...string) {
	t.Helper()
	prev := -1
	for _, eventType := range eventTypes {
		needle := `"type":"` + eventType + `"`
		index := strings.Index(body, needle)
		if index < 0 {
			t.Fatalf("expected event %s in stream body: %s", eventType, body)
		}
		if index <= prev {
			t.Fatalf("expected event order %v in stream body: %s", eventTypes, body)
		}
		prev = index
	}
}
