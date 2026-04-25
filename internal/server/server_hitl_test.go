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

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/contracts"
)

func TestFrontendSubmitAndSteerAreConsumedBeforeNextTurn(t *testing.T) {
	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)

	fixture := newTestFixtureWithModelHandler(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"tool_question","type":"function","function":{"name":"ask_user_question","arguments":"{\"mode\":\"question\",\"questions\":[{\"question\":\"Need confirmation\",\"type\":\"select\",\"options\":[{\"label\":\"Approve\",\"description\":\"Continue with the request\"}],\"allowFreeText\":false}]}"}}]},"finish_reason":"tool_calls"}]}`,
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

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please confirm first"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	toolID := ""
	var toolStartPayload map[string]any
	var awaitQuestionPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "tool.start" && payload["toolName"] == "ask_user_question" {
				toolStartPayload = payload
				toolID, _ = payload["toolId"].(string)
			}
			if payload["type"] == "awaiting.ask" {
				awaitQuestionPayload = payload
				runID, _ = payload["runId"].(string)
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}
	if toolStartPayload == nil {
		t.Fatalf("expected frontend tool.start before awaiting.ask, got %s", streamBody.String())
	}
	if _, exists := toolStartPayload["toolType"]; exists {
		t.Fatalf("did not expect toolType on tool.start, got %#v", toolStartPayload)
	}
	if _, exists := toolStartPayload["viewportKey"]; exists {
		t.Fatalf("did not expect viewportKey on tool.start, got %#v", toolStartPayload)
	}
	if _, exists := toolStartPayload["toolTimeout"]; exists {
		t.Fatalf("did not expect toolTimeout on tool.start, got %#v", toolStartPayload)
	}
	if awaitQuestionPayload == nil {
		t.Fatalf("expected awaiting.ask before submit, got %s", streamBody.String())
	}
	if awaitQuestionPayload["awaitingId"] != toolID {
		t.Fatalf("expected awaitingId to match toolId, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["viewportType"]; exists {
		t.Fatalf("did not expect viewportType on question awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["viewportKey"]; exists {
		t.Fatalf("did not expect viewportKey on question awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if _, exists := awaitQuestionPayload["toolTimeout"]; exists {
		t.Fatalf("did not expect toolTimeout on awaiting.ask, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["timeout"] != float64(210000) {
		t.Fatalf("expected await question timeout 210000, got %#v", awaitQuestionPayload)
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

	steerReq := httptest.NewRequest(http.MethodPost, "/api/steer", bytes.NewBufferString(`{"runId":"`+runID+`","message":"Please keep it short."}`))
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

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","awaitingId":"`+toolID+`","params":[{"id":"q1","answer":"Approve"}]}`))
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
	if !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected tool.result event, got %s", body)
	}
	if !strings.Contains(body, `"mode":"question"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"answers":[{"answer":"Approve","id":"q1","question":"Need confirmation"}]`) {
		t.Fatalf("expected normalized question awaiting.answer, got %s", body)
	}
	if !strings.Contains(body, `"result":[{"id":"q1","answer":"Approve"}]`) {
		t.Fatalf("expected raw submit array in tool.result, got %s", body)
	}
	if !strings.Contains(body, "final answer") {
		t.Fatalf("expected final answer in stream, got %s", body)
	}
	assertEventOrder(t, body, "tool.start", "awaiting.ask", "tool.args", "tool.end", "request.submit", "awaiting.answer", "tool.result", "request.steer")

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
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatsResp.Data[0].ChatID, nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	foundFrontendSnapshot := false
	foundAwaitAsk := false
	foundRequestSubmit := false
	foundAwaitingAnswer := false
	for _, event := range chatResp.Data.Events {
		switch event.Type {
		case "tool.snapshot":
			if event.String("toolName") != "ask_user_question" {
				continue
			}
			foundFrontendSnapshot = true
			if _, exists := event.Payload["toolType"]; exists {
				t.Fatalf("did not expect frontend snapshot toolType, got %#v", event)
			}
			if _, exists := event.Payload["viewportKey"]; exists {
				t.Fatalf("did not expect frontend snapshot viewportKey, got %#v", event)
			}
			if _, exists := event.Payload["toolTimeout"]; exists {
				t.Fatalf("did not expect frontend snapshot toolTimeout, got %#v", event)
			}
		case "awaiting.ask":
			foundAwaitAsk = true
			if event.String("mode") != "question" || event.Value("viewportKey") != nil || event.Value("viewportType") != nil {
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
	if !foundFrontendSnapshot {
		t.Fatalf("expected ask_user_question tool.snapshot in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundAwaitAsk {
		t.Fatalf("expected awaiting.ask in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundRequestSubmit {
		t.Fatalf("expected request.submit in chat detail, got %#v", chatResp.Data.Events)
	}
	if !foundAwaitingAnswer {
		t.Fatalf("expected awaiting.answer in chat detail, got %#v", chatResp.Data.Events)
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

func TestQuestionAwaitFollowsToolStartAndPrecedesToolArgs(t *testing.T) {
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

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"ask me a few things"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	toolID := ""
	var awaitQuestionPayload map[string]any
	var toolStartPayload map[string]any
	var toolResultPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "awaiting.ask":
				awaitQuestionPayload = payload
				runID, _ = payload["runId"].(string)
				goto questionSubmit
			case "tool.start":
				if payload["toolName"] == "ask_user_question" {
					toolStartPayload = payload
					toolID, _ = payload["toolId"].(string)
				}
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

questionSubmit:
	if awaitQuestionPayload == nil {
		t.Fatalf("expected awaiting.ask after tool.start and before tool.args, got %s", streamBody.String())
	}
	if toolStartPayload == nil {
		t.Fatalf("expected tool.start for ask_user_question, got %s", streamBody.String())
	}
	if awaitQuestionPayload["awaitingId"] != toolID {
		t.Fatalf("expected awaitingId to match toolId, got %#v", awaitQuestionPayload)
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

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","awaitingId":"`+toolID+`","params":[{"id":"q1","answers":["产品更新","使用教程"]},{"id":"q2","answer":2}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "tool.result" {
				toolResultPayload = payload
			}
		}
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
	if !strings.Contains(body, `"type":"tool.result"`) {
		t.Fatalf("expected tool.result event, got %s", body)
	}
	if strings.Contains(body, `"result":{"mode":"question"`) {
		t.Fatalf("did not expect normalized question wrapper in tool.result, got %s", body)
	}
	if toolResultPayload == nil {
		t.Fatalf("expected tool.result payload, got %s", body)
	}
	resultItems, ok := toolResultPayload["result"].([]any)
	if !ok || len(resultItems) != 2 {
		t.Fatalf("expected raw submit array in tool.result, got %#v", toolResultPayload)
	}
	firstItem, _ := resultItems[0].(map[string]any)
	secondItem, _ := resultItems[1].(map[string]any)
	firstAnswers, _ := firstItem["answers"].([]any)
	if firstItem["id"] != "q1" || len(firstAnswers) != 2 || firstAnswers[0] != "产品更新" || firstAnswers[1] != "使用教程" {
		t.Fatalf("unexpected first tool.result item: %#v", firstItem)
	}
	if secondItem["id"] != "q2" || secondItem["answer"] != float64(2) {
		t.Fatalf("unexpected second tool.result item: %#v", secondItem)
	}
	assertEventOrder(t, body, "tool.start", "awaiting.ask", "tool.args", "tool.end", "request.submit", "awaiting.answer", "tool.result")

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

func TestQuestionChunkedArgsEmitAwaitAfterFirstToolArgs(t *testing.T) {
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

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"ask me a few things"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	runID := ""
	toolID := ""
	var awaitQuestionPayload map[string]any
	var toolStartPayload map[string]any
	var toolResultPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "awaiting.ask":
				awaitQuestionPayload = payload
				runID, _ = payload["runId"].(string)
				goto chunkedQuestionSubmit
			case "tool.start":
				if payload["toolName"] == "ask_user_question" {
					toolStartPayload = payload
					toolID, _ = payload["toolId"].(string)
				}
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
	if toolStartPayload == nil {
		t.Fatalf("expected tool.start for ask_user_question, got %s", streamBody.String())
	}
	if awaitQuestionPayload["awaitingId"] != toolID {
		t.Fatalf("expected awaitingId to match toolId, got %#v", awaitQuestionPayload)
	}
	if awaitQuestionPayload["mode"] != "question" {
		t.Fatalf("expected question mode, got %#v", awaitQuestionPayload)
	}
	questions, _ := awaitQuestionPayload["questions"].([]any)
	if len(questions) != 2 {
		t.Fatalf("expected inline questions on question-mode awaiting.ask, got %#v", awaitQuestionPayload)
	}

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","awaitingId":"`+toolID+`","params":[{"id":"q1","answers":["产品更新","使用教程"]},{"id":"q2","answer":2}]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "tool.result" {
				toolResultPayload = payload
			}
		}
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
	if !strings.Contains(body, `"chunkIndex":0`) || !strings.Contains(body, `"chunkIndex":1`) {
		t.Fatalf("expected tool.args chunks 0 and 1, got %s", body)
	}
	firstToolArgsIndex := strings.Index(body, `"chunkIndex":0`)
	awaitAskIndex := strings.Index(body, `"type":"awaiting.ask"`)
	secondToolArgsIndex := strings.Index(body, `"chunkIndex":1`)
	toolEndIndex := strings.Index(body, `"type":"tool.end"`)
	if firstToolArgsIndex < 0 || awaitAskIndex < 0 || secondToolArgsIndex < 0 || toolEndIndex < 0 {
		t.Fatalf("expected chunked question flow markers, got %s", body)
	}
	if !(firstToolArgsIndex < awaitAskIndex && awaitAskIndex < secondToolArgsIndex && secondToolArgsIndex < toolEndIndex) {
		t.Fatalf("expected chunked event order tool.args(0) -> awaiting.ask -> tool.args(1) -> tool.end, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) {
		t.Fatalf("expected request.submit event, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) {
		t.Fatalf("expected awaiting.answer event, got %s", body)
	}
	if toolResultPayload == nil {
		t.Fatalf("expected tool.result payload, got %s", body)
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

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"ask me a question"}`))
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
	if !strings.Contains(body, `"type":"tool.start"`) {
		t.Fatalf("expected tool.start event, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.args"`) {
		t.Fatalf("expected tool.args event, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.end"`) {
		t.Fatalf("expected tool.end event, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.ask"`) {
		t.Fatalf("did not expect awaiting.ask for invalid question args, got %s", body)
	}
	if strings.Contains(body, `"type":"awaiting.payload"`) {
		t.Fatalf("did not expect awaiting.payload for invalid question args, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool.result"`) || !strings.Contains(body, `invalid tool arguments: Pick a plan: options is required for select and multi-select questions`) {
		t.Fatalf("expected invalid tool arguments tool.result, got %s", body)
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

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"ask me a question"}`))
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

	submitReq := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+runID+`","awaitingId":"`+toolID+`","params":[]}`))
	submitReq.Header.Set("Content-Type", "application/json")
	submitRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	var toolResultPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			if payload["type"] == "tool.result" {
				toolResultPayload = payload
			}
		}
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
	if toolResultPayload == nil {
		t.Fatalf("expected tool.result payload, got %s", body)
	}
	if toolResultPayload["result"] != nil {
		t.Fatalf("expected dismissed tool.result payload to be omitted, got %#v", toolResultPayload)
	}
	assertEventOrder(t, body, "tool.start", "awaiting.ask", "tool.args", "tool.end", "request.submit", "awaiting.answer", "tool.result")

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

type recordingMCPClient struct {
	commands []string
}

type stubMCPToolCatalog struct {
	defs []api.ToolDetailResponse
}

func (s *recordingSandbox) OpenIfNeeded(_ context.Context, _ *contracts.ExecutionContext) error {
	return nil
}

func (s *recordingSandbox) Execute(_ context.Context, _ *contracts.ExecutionContext, command string, cwd string, _ int64, env map[string]string) (contracts.SandboxExecutionResult, error) {
	s.commands = append(s.commands, command)
	s.envs = append(s.envs, contracts.CloneStringMap(env))
	return contracts.SandboxExecutionResult{
		ExitCode:         0,
		Stdout:           "executed: " + command,
		Stderr:           "",
		WorkingDirectory: cwd,
	}, nil
}

func (s *recordingSandbox) CloseQuietly(_ *contracts.ExecutionContext) {}

func (s *scriptedSandbox) OpenIfNeeded(_ context.Context, _ *contracts.ExecutionContext) error {
	return nil
}

func (s *scriptedSandbox) Execute(_ context.Context, _ *contracts.ExecutionContext, command string, cwd string, _ int64, env map[string]string) (contracts.SandboxExecutionResult, error) {
	if s.execute == nil {
		return contracts.SandboxExecutionResult{ExitCode: 0, WorkingDirectory: cwd}, nil
	}
	return s.execute(command, cwd, env), nil
}

func (s *scriptedSandbox) CloseQuietly(_ *contracts.ExecutionContext) {}

func (m *recordingMCPClient) CallTool(_ context.Context, _ string, toolName string, args map[string]any, _ map[string]any) (any, error) {
	command, _ := args["command"].(string)
	m.commands = append(m.commands, command)
	return map[string]any{
		"structuredContent": map[string]any{
			"tool":    toolName,
			"command": command,
			"status":  "ok",
		},
	}, nil
}

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

func TestBashHITLApproveFlow(t *testing.T) {
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{action: "approve"})
	expectedCommand := rebuildPayloadCommandForTest(t, defaultBashHITLCommand(), payloadFromCommandForTest(t, defaultBashHITLCommand()))
	expectedAwaitPayload, err := json.Marshal(payloadFromCommandForTest(t, defaultBashHITLCommand()))
	if err != nil {
		t.Fatalf("marshal expected await payload: %v", err)
	}
	expectedSubmitPayload := string(expectedAwaitPayload)
	if len(executed) != 1 || executed[0] != expectedCommand {
		t.Fatalf("expected approved command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"viewportKey":"leave_form"`) {
		t.Fatalf("expected leave_form viewport in stream, got %s", body)
	}
	if !strings.Contains(body, `"mode":"form"`) || !strings.Contains(body, `"forms":[`) {
		t.Fatalf("expected form awaiting.ask payload in stream, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"action":"submit"`) ||
		!strings.Contains(body, `"id":"form-1"`) ||
		!strings.Contains(body, `"form":`+expectedSubmitPayload) {
		t.Fatalf("expected approve awaiting.answer in stream, got %s", body)
	}
	if !strings.Contains(body, `"form":`+string(expectedAwaitPayload)) {
		t.Fatalf("expected form awaiting.ask payload in stream, got %s", body)
	}
	if !strings.Contains(body, `"title":"mock 请假申请"`) {
		t.Fatalf("expected form awaiting.ask title in stream, got %s", body)
	}
	if !strings.Contains(body, `"leave_type":"annual"`) ||
		!strings.Contains(body, `"start_date":"2026-04-20"`) ||
		!strings.Contains(body, `"end_date":"2026-04-22"`) {
		t.Fatalf("expected canonical snake_case leave payload in stream, got %s", body)
	}
	if strings.Contains(body, `"type":"annual"`) ||
		strings.Contains(body, `"startDate":"2026-04-20"`) ||
		strings.Contains(body, `"endDate":"2026-04-22"`) {
		t.Fatalf("did not expect camelCase leave payload aliases in stream, got %s", body)
	}
	if strings.Contains(body, `"initialPayload":`) || strings.Contains(body, `"viewportPayload":`) {
		t.Fatalf("did not expect legacy form payload fields in stream, got %s", body)
	}
	if strings.Contains(body, "map[") {
		t.Fatalf("did not expect Go map string in stream, got %s", body)
	}
}

func TestBashHITLApproveFlowReplaysApprovalSummaryInChatRawMessages(t *testing.T) {
	var providerCallCount atomic.Int32
	command := "docker rmi nginx:latest"
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
			cfg.BashHITL.DefaultTimeoutMs = 15000
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			root := filepath.Join(cfg.Paths.SkillsMarketDir, "mock-skill", ".bash-hooks")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("mkdir skill bash-hooks dir: %v", err)
			}
			rulesContent := strings.Join([]string{
				"commands:",
				"  - command: docker",
				"    subcommands:",
				"      - match: rmi",
				"        level: 1",
				"        viewportType: builtin",
				"        viewportKey: confirm_dialog",
				"        ruleKey: dangerous-commands::docker-rmi",
			}, "\n")
			if err := os.WriteFile(filepath.Join(root, "dangerous.yml"), []byte(rulesContent), 0o644); err != nil {
				t.Fatalf("write skill bash hook rule: %v", err)
			}
		},
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please push the change"}`))
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
	fixture.server.ServeHTTP(submitRec, httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+extractRunIDFromStream(t, streamBody.String())+`","awaitingId":"`+awaitingID+`","params":[{"id":"`+approvalID+`","decision":"approve"}]}`)))
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
	fixture.server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+chatID+"&includeRawMessages=true", nil))
	var chatResp api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &chatResp); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}

	hitlIndex := -1
	hitlCount := 0
	for i, message := range chatResp.Data.RawMessages {
		if message["role"] == "user" && strings.Contains(stringValue(message["content"]), "[HITL]") {
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
	if !strings.Contains(stringValue(chatResp.Data.RawMessages[hitlIndex]["content"]), `[HITL] docker rmi nginx:latest → approve`) {
		t.Fatalf("expected replayed HITL summary content, got %#v", chatResp.Data.RawMessages[hitlIndex])
	}
}

func TestSandboxBashResultShapeAcrossStreamBoundaries(t *testing.T) {
	t.Run("success uses plain stdout for sse and tool message", func(t *testing.T) {
		body, secondTurn := runSandboxBashQueryForResultShape(t, &scriptedSandbox{
			execute: func(command string, cwd string, _ map[string]string) contracts.SandboxExecutionResult {
				return contracts.SandboxExecutionResult{
					ExitCode:         0,
					Stdout:           "listed from " + cwd + ": " + command + "\n",
					WorkingDirectory: cwd,
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
					ExitCode:         2,
					Stdout:           "",
					Stderr:           "ls: sample: No such file or directory\n",
					WorkingDirectory: cwd,
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

	t.Run("html hitl success keeps stdout in result without approval sidecar", func(t *testing.T) {
		body, _ := runBashHITLFlow(t, bashHITLFlowOptions{action: "approve"})

		resultPayload := findToolResultPayload(t, body, "tool_bash")
		if got, ok := resultPayload["result"].(string); !ok || got == "" {
			t.Fatalf("expected stdout string tool.result payload, got %#v", resultPayload["result"])
		}
		if _, ok := resultPayload["approval"]; ok {
			t.Fatalf("did not expect approval sidecar for html form HITL, got %#v", resultPayload)
		}
	})
}

func TestBashHITLModifyFlow(t *testing.T) {
	modified := `mock create-leave --payload {"applicant_id":"E1001","department_id":"engineering","leave_type":"personal","start_date":"2026-04-21","end_date":"2026-04-22","days":2,"reason":"family_trip"}`
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{action: "modify", modifiedCommand: modified})
	expectedCommand := rebuildPayloadCommandForTest(t, defaultBashHITLCommand(), payloadFromCommandForTest(t, modified))
	expectedSubmitPayload, err := json.Marshal(payloadFromCommandForTest(t, modified))
	if err != nil {
		t.Fatalf("marshal modified payload: %v", err)
	}
	if len(executed) != 1 || executed[0] != expectedCommand {
		t.Fatalf("expected modified command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"action":"submit"`) ||
		!strings.Contains(body, `"id":"form-1"`) ||
		!strings.Contains(body, `"form":`+string(expectedSubmitPayload)) {
		t.Fatalf("expected modify awaiting.answer in stream, got %s", body)
	}
	if strings.Contains(body, "map[") {
		t.Fatalf("did not expect Go map string in stream, got %s", body)
	}
}

func TestBashHITLRejectFlow(t *testing.T) {
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{action: "reject"})
	if len(executed) != 0 {
		t.Fatalf("expected rejected command not to execute, got %#v", executed)
	}
	resultPayload := findToolResultPayload(t, body, "tool_bash")
	if got, ok := resultPayload["result"].(string); !ok || got != "user_rejected: User rejected this command. Do NOT retry with a different command. End the turn now." {
		t.Fatalf("expected hard-stop rejected tool result, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"action":"reject"`) ||
		!strings.Contains(body, `"id":"form-1"`) {
		t.Fatalf("expected reject awaiting.answer in stream, got %s", body)
	}
	if strings.Contains(body, "map[") {
		t.Fatalf("did not expect Go map string in stream, got %s", body)
	}
}

func TestBashHITLCancelFlow(t *testing.T) {
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{action: "cancel"})
	if len(executed) != 0 {
		t.Fatalf("expected cancelled command not to execute, got %#v", executed)
	}
	resultPayload := findToolResultPayload(t, body, "tool_bash")
	if got, ok := resultPayload["result"].(string); !ok || got != "user_rejected: User rejected this command. Do NOT retry with a different command. End the turn now." {
		t.Fatalf("expected hard-stop cancelled tool result, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) || !strings.Contains(body, `"action":"cancel"`) {
		t.Fatalf("expected cancel request.submit payload in stream, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"action":"cancel"`) ||
		!strings.Contains(body, `"id":"form-1"`) {
		t.Fatalf("expected cancel awaiting.answer in stream, got %s", body)
	}
}

func TestBashHITLTimeoutFlow(t *testing.T) {
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		skipSubmit: true,
		timeoutMs:  20,
	})
	if len(executed) != 0 {
		t.Fatalf("expected timed out command not to execute, got %#v", executed)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"error"`) ||
		!strings.Contains(body, `"code":"timeout"`) {
		t.Fatalf("expected timeout awaiting.answer in stream, got %s", body)
	}
	resultPayload := findToolResultPayload(t, body, "tool_bash")
	if got, ok := resultPayload["result"].(string); !ok || got != "hitl_timeout: command execution timed out while waiting for user approval" {
		t.Fatalf("expected timeout tool.result in stream, got %s", body)
	}
	if strings.Contains(body, "map[") {
		t.Fatalf("did not expect Go map string in stream, got %s", body)
	}
}

func TestBashHITLSimpleBashApproveFlow(t *testing.T) {
	mcpClient := &recordingMCPClient{}
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		toolName: "simple-bash",
		action:   "approve",
		mcp:      mcpClient,
		mcpTools: stubMCPToolCatalog{defs: []api.ToolDetailResponse{
			{
				Key:         "simple-bash",
				Name:        "simple-bash",
				Label:       "Simple Bash",
				Description: "Execute mock bash command",
				Parameters:  map[string]any{"type": "object"},
				Meta: map[string]any{
					"kind":          "backend",
					"sourceType":    "mcp",
					"sourceKey":     "mock",
					"serverKey":     "mock",
					"clientVisible": true,
				},
			},
		}},
	})
	expectedCommand := rebuildPayloadCommandForTest(t, defaultBashHITLCommand(), payloadFromCommandForTest(t, defaultBashHITLCommand()))
	if len(executed) != 1 || executed[0] != expectedCommand {
		t.Fatalf("expected simple-bash command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"viewportKey":"leave_form"`) {
		t.Fatalf("expected leave_form viewport in stream, got %s", body)
	}
}

func TestBashHITLApproveFlowForExpenseCreate(t *testing.T) {
	command := `mock expense add --payload {"employee":{"id":"E1001","name":"张三"},"department":{"code":"engineering","name":"工程部"},"expense_type":"travel","currency":"CNY","total_amount":1280.5,"items":[{"category":"transport","amount":800,"invoice_id":"INV-001","occurred_on":"2026-04-10","description":"flight"},{"category":"hotel","amount":480.5,"invoice_id":"INV-002","occurred_on":"2026-04-11","description":"hotel"}],"submitted_at":"2026-04-14T10:30:00+08:00"}`
	rules := strings.Join([]string{
		"commands:",
		"  - command: mock",
		"    subcommands:",
		"      - match: expense add",
		"        level: 1",
		"        viewportType: html",
		"        viewportKey: expense_form",
	}, "\n")
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		action:       "approve",
		command:      command,
		rulesContent: rules,
	})
	expectedCommand := rebuildPayloadCommandForTest(t, command, payloadFromCommandForTest(t, command))
	expectedAwaitPayload, err := json.Marshal(payloadFromCommandForTest(t, command))
	if err != nil {
		t.Fatalf("marshal expected expense await payload: %v", err)
	}
	if len(executed) != 1 || executed[0] != expectedCommand {
		t.Fatalf("expected approved expense command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"viewportKey":"expense_form"`) {
		t.Fatalf("expected expense_form viewport in stream, got %s", body)
	}
	if !strings.Contains(body, `"form":`+string(expectedAwaitPayload)) {
		t.Fatalf("expected expense approval payload in stream, got %s", body)
	}
}

func TestBashHITLApproveFlowForProcurementCreate(t *testing.T) {
	command := `mock procurement create --payload {"requester_id":"E1001","department":"engineering","budget_code":"RD-2026-001","reason":"team expansion","delivery_city":"Shanghai","items":[{"name":"MacBook Pro","quantity":2,"unit_price":18999,"vendor":"Apple"}],"approvers":["MGR100","FIN200"],"requested_at":"2026-04-14T11:00:00+08:00"}`
	rules := strings.Join([]string{
		"commands:",
		"  - command: mock",
		"    subcommands:",
		"      - match: procurement create",
		"        level: 1",
		"        viewportType: html",
		"        viewportKey: procurement_form",
	}, "\n")
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		action:       "approve",
		command:      command,
		rulesContent: rules,
	})
	expectedCommand := rebuildPayloadCommandForTest(t, command, payloadFromCommandForTest(t, command))
	expectedAwaitPayload, err := json.Marshal(payloadFromCommandForTest(t, command))
	if err != nil {
		t.Fatalf("marshal expected procurement await payload: %v", err)
	}
	if len(executed) != 1 || executed[0] != expectedCommand {
		t.Fatalf("expected approved procurement command to execute once, got %#v", executed)
	}
	if !strings.Contains(body, `"viewportKey":"procurement_form"`) {
		t.Fatalf("expected procurement_form viewport in stream, got %s", body)
	}
	if !strings.Contains(body, `"form":`+string(expectedAwaitPayload)) {
		t.Fatalf("expected procurement approval payload in stream, got %s", body)
	}
}

func TestBashHITLDockerRMIApproveFlow(t *testing.T) {
	command := "docker rmi nginx:latest"
	rules := strings.Join([]string{
		"commands:",
		"  - command: docker",
		"    subcommands:",
		"      - match: rmi",
		"        level: 1",
		"        viewportType: builtin",
		"        viewportKey: confirm_dialog",
	}, "\n")
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		action:       "approve",
		command:      command,
		rulesContent: rules,
	})
	if len(executed) != 1 || executed[0] != command {
		t.Fatalf("expected approved docker rmi to execute once, got %#v", executed)
	}
	if strings.Contains(body, `"viewportKey":"confirm_dialog"`) {
		t.Fatalf("did not expect confirm_dialog viewport in stream, got %s", body)
	}
	if !strings.Contains(body, `"mode":"approval"`) ||
		!strings.Contains(body, `"approvals":[`) ||
		!strings.Contains(body, `"command":"docker rmi nginx:latest"`) ||
		!strings.Contains(body, `"ruleKey":"dangerous::docker::rmi::1::builtin::confirm_dialog"`) ||
		!strings.Contains(body, `"id":"tool_bash"`) ||
		!strings.Contains(body, `"description":"`) ||
		!strings.Contains(body, `"allowFreeText":true`) {
		t.Fatalf("expected approval awaiting.ask payload in stream, got %s", body)
	}
	if strings.Contains(body, `"level":1`) {
		t.Fatalf("did not expect level in approval awaiting.ask payload, got %s", body)
	}
	if !strings.Contains(body, `"type":"request.submit"`) ||
		!strings.Contains(body, `"params":[{"id":"tool_bash","decision":"approve"}]`) {
		t.Fatalf("expected approval request.submit payload in stream, got %s", body)
	}
	if !strings.Contains(body, `"type":"awaiting.answer"`) ||
		!strings.Contains(body, `"status":"answered"`) ||
		!strings.Contains(body, `"decision":"approve"`) ||
		!strings.Contains(body, `"id":"tool_bash"`) ||
		!strings.Contains(body, `"command":"docker rmi nginx:latest"`) {
		t.Fatalf("expected normalized approval awaiting.answer payload in stream, got %s", body)
	}
	resultPayload := findToolResultPayload(t, body, "tool_bash")
	if got, ok := resultPayload["result"].(string); !ok || got == "" {
		t.Fatalf("expected stdout string tool.result payload, got %#v", resultPayload["result"])
	}
	approvalPayload, ok := resultPayload["approval"].(map[string]any)
	if !ok || approvalPayload["decision"] != "approve" {
		t.Fatalf("expected approval sidecar on tool.result, got %#v", resultPayload)
	}
	if _, ok := resultPayload["hitl"]; ok {
		t.Fatalf("did not expect legacy hitl key, got %#v", resultPayload)
	}
	if strings.Contains(body, `"frontend_submit_invalid_payload"`) {
		t.Fatalf("did not expect frontend_submit_invalid_payload, got %s", body)
	}
}

func TestBashHITLDockerImageRMRejectFlow(t *testing.T) {
	command := "docker image rm nginx:latest"
	rules := strings.Join([]string{
		"commands:",
		"  - command: docker",
		"    subcommands:",
		"      - match: image rm",
		"        level: 1",
		"        viewportType: builtin",
		"        viewportKey: confirm_dialog",
	}, "\n")
	body, executed := runBashHITLFlow(t, bashHITLFlowOptions{
		action:       "reject",
		command:      command,
		rulesContent: rules,
	})
	if len(executed) != 0 {
		t.Fatalf("expected rejected docker image rm not to execute, got %#v", executed)
	}
	resultPayload := findToolResultPayload(t, body, "tool_bash")
	if got, ok := resultPayload["result"].(string); !ok || got != "user_rejected: User rejected this command. Do NOT retry with a different command. End the turn now." {
		t.Fatalf("expected hard-stop rejected tool result, got %s", body)
	}
	if strings.Contains(body, `"viewportKey":"confirm_dialog"`) {
		t.Fatalf("did not expect confirm_dialog viewport in stream, got %s", body)
	}
	if !strings.Contains(body, `"decision":"reject"`) ||
		!strings.Contains(body, `"id":"tool_bash"`) ||
		!strings.Contains(body, `"command":"docker image rm nginx:latest"`) {
		t.Fatalf("expected reject approval answer in stream, got %s", body)
	}
}

type bashHITLFlowOptions struct {
	toolName        string
	action          string
	modifiedCommand string
	command         string
	rulesContent    string
	skipSubmit      bool
	timeoutMs       int
	mcp             contracts.McpClient
	mcpTools        stubMCPToolCatalog
}

func runBashHITLFlow(t *testing.T, options bashHITLFlowOptions) (string, []string) {
	t.Helper()
	toolName := options.toolName
	if toolName == "" {
		toolName = "bash"
	}
	command := defaultBashHITLCommand()
	if strings.TrimSpace(options.command) != "" {
		command = options.command
	}
	rulesContent := strings.Join([]string{
		"commands:",
		"  - command: mock",
		"    subcommands:",
		"      - match: create-leave",
		"        level: 1",
		"        title: mock 请假申请",
		"        viewportType: html",
		"        viewportKey: leave_form",
	}, "\n")
	if strings.TrimSpace(options.rulesContent) != "" {
		rulesContent = options.rulesContent
	}

	var providerCallCount atomic.Int32
	secondTurnMessages := make(chan []map[string]any, 1)
	sandbox := &recordingSandbox{}
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		call := providerCallCount.Add(1)
		switch call {
		case 1:
			writeProviderSSE(t, w,
				providerToolCallFrame(t, "tool_bash", toolName, map[string]any{
					"command":     command,
					"description": "执行测试命令",
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
				`{"choices":[{"delta":{"content":"bash hitl complete"},"finish_reason":"stop"}]}`,
				`[DONE]`,
			)
		default:
			t.Fatalf("unexpected provider call %d", call)
		}
	}, testFixtureOptions{
		sandbox:  sandbox,
		mcp:      options.mcp,
		mcpTools: options.mcpTools,
		configure: func(cfg *config.Config) {
			cfg.BashHITL.DefaultTimeoutMs = 15000
			if options.timeoutMs > 0 {
				cfg.BashHITL.DefaultTimeoutMs = options.timeoutMs
			}
		},
		setupRuntime: func(_ string, cfg *config.Config) {
			root := filepath.Join(cfg.Paths.SkillsMarketDir, "mock-skill", ".bash-hooks")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatalf("mkdir skill bash-hooks dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, "dangerous.yml"), []byte(rulesContent), 0o644); err != nil {
				t.Fatalf("write skill bash hook rule: %v", err)
			}
		},
	})

	httpServer := newLoopbackServer(t, fixture.server)
	defer httpServer.Close()

	resp, err := http.Post(httpServer.URL+"/api/query", "application/json", bytes.NewBufferString(`{"message":"please push the change"}`))
	if err != nil {
		t.Fatalf("post query: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var streamBody strings.Builder
	originalToolID := ""
	awaitingID := ""
	approvalID := ""
	var awaitAskPayload map[string]any
	for {
		line, readErr := reader.ReadString('\n')
		streamBody.WriteString(line)
		if strings.HasPrefix(line, "data: {") {
			payload := decodeSSELine(t, line)
			switch payload["type"] {
			case "tool.start":
				switch payload["toolName"] {
				case "bash":
					originalToolID, _ = payload["toolId"].(string)
				case "simple-bash":
					originalToolID, _ = payload["toolId"].(string)
				}
			case "awaiting.ask":
				awaitAskPayload = payload
				awaitingID, _ = payload["awaitingId"].(string)
				if approvals, ok := payload["approvals"].([]any); ok && len(approvals) > 0 {
					if firstApproval, ok := approvals[0].(map[string]any); ok {
						approvalID, _ = firstApproval["id"].(string)
					}
				}
				goto submit
			}
		}
		if readErr != nil {
			t.Fatalf("read query stream before submit: %v", readErr)
		}
	}

submit:
	if !options.skipSubmit {
		var submitPayload string
		if strings.EqualFold(stringValue(awaitAskPayload["mode"]), "form") {
			if options.action == "reject" {
				submitPayload = `[{"id":"form-1","action":"reject"}]`
			} else if options.action == "cancel" {
				submitPayload = `[{"id":"form-1","action":"cancel"}]`
			} else {
				submitCommand := command
				if options.action == "modify" {
					submitCommand = options.modifiedCommand
				}
				payloadJSON, err := json.Marshal([]map[string]any{{
					"id":     "form-1",
					"action": "submit",
					"form":   payloadFromCommandForTest(t, submitCommand),
				}})
				if err != nil {
					t.Fatalf("marshal html submit payload: %v", err)
				}
				submitPayload = string(payloadJSON)
			}
		} else {
			if strings.TrimSpace(approvalID) == "" {
				t.Fatalf("expected approval id in awaiting.ask payload, got %#v", awaitAskPayload)
			}
			submitPayload = `[{"id":"` + approvalID + `","decision":"` + options.action + `"}]`
		}
		submitRec := httptest.NewRecorder()
		fixture.server.ServeHTTP(submitRec, httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"`+extractRunIDFromStream(t, streamBody.String())+`","awaitingId":"`+awaitingID+`","params":`+submitPayload+`}`)))
		if submitRec.Code != http.StatusOK {
			t.Fatalf("submit expected 200, got %d: %s", submitRec.Code, submitRec.Body.String())
		}
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

	messages := decodeSSEMessages(t, streamBody.String())
	assertSpecificEventOrder(t, messages, originalToolID, awaitingID)
	select {
	case secondTurn := <-secondTurnMessages:
		toolMessages := 0
		hitlSummaries := 0
		seenUserAfterTool := false
		for _, message := range secondTurn {
			role, _ := message["role"].(string)
			if role == "tool" {
				toolMessages++
				if seenUserAfterTool {
					t.Fatalf("expected tool results to stay contiguous before HITL summary, got %#v", secondTurn)
				}
				continue
			}
			if role == "user" {
				content, _ := message["content"].(string)
				if strings.Contains(content, "[HITL]") {
					hitlSummaries++
					seenUserAfterTool = true
				}
			}
		}
		if toolMessages < 1 {
			t.Fatalf("expected second turn to receive original bash tool result, got %#v", secondTurn)
		}
		if hitlSummaries != 1 {
			t.Fatalf("expected one HITL summary user message, got %#v", secondTurn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second provider request")
	}

	if toolName == "simple-bash" {
		client, ok := options.mcp.(*recordingMCPClient)
		if !ok {
			return streamBody.String(), nil
		}
		return streamBody.String(), append([]string(nil), client.commands...)
	}
	return streamBody.String(), append([]string(nil), sandbox.commands...)
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

	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewBufferString(`{"message":"list sample","agentKey":"mock-runner"}`))
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

func defaultBashHITLCommand() string {
	return `mock create-leave --payload {"applicant_id":"E1001","department_id":"engineering","leave_type":"annual","start_date":"2026-04-20","end_date":"2026-04-22","days":3,"reason":"family_trip"}`
}

func payloadFromCommandForTest(t *testing.T, command string) map[string]any {
	t.Helper()
	idx := strings.Index(command, "--payload ")
	if idx < 0 {
		t.Fatalf("expected --payload in command %q", command)
	}
	raw := strings.TrimSpace(command[idx+len("--payload "):])
	if strings.HasPrefix(raw, "'") && strings.HasSuffix(raw, "'") && len(raw) >= 2 {
		raw = raw[1 : len(raw)-1]
		raw = strings.ReplaceAll(raw, `'"'"'`, `'`)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("decode payload from command %q: %v", command, err)
	}
	return payload
}

func rebuildPayloadCommandForTest(t *testing.T, originalCommand string, payload map[string]any) string {
	t.Helper()
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	idx := strings.Index(originalCommand, "--payload ")
	if idx < 0 {
		t.Fatalf("expected --payload in command %q", originalCommand)
	}
	return originalCommand[:idx+len("--payload ")] + "'" + strings.ReplaceAll(string(payloadJSON), "'", `'"'"'`) + "'"
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

func assertSpecificEventOrder(t *testing.T, messages []map[string]any, originalToolID string, awaitingID string) {
	t.Helper()
	originalStart := -1
	awaitAsk := -1
	requestSubmit := -1
	awaitingAnswer := -1
	originalResult := -1
	for idx, message := range messages {
		eventType, _ := message["type"].(string)
		switch eventType {
		case "tool.start":
			if message["toolId"] == originalToolID {
				originalStart = idx
			}
		case "awaiting.ask":
			if message["awaitingId"] == awaitingID {
				awaitAsk = idx
			}
		case "request.submit":
			if message["awaitingId"] == awaitingID {
				requestSubmit = idx
			}
		case "awaiting.answer":
			if message["awaitingId"] == awaitingID {
				awaitingAnswer = idx
			}
		case "tool.result":
			if message["toolId"] == originalToolID {
				originalResult = idx
			}
		}
	}
	if requestSubmit >= 0 {
		if !(originalStart >= 0 && awaitAsk > originalStart && requestSubmit > awaitAsk && awaitingAnswer > requestSubmit && originalResult > awaitingAnswer) {
			t.Fatalf("unexpected HITL event order: %#v", messages)
		}
		return
	}
	if !(originalStart >= 0 && awaitAsk > originalStart && awaitingAnswer > awaitAsk && originalResult > awaitingAnswer) {
		t.Fatalf("unexpected HITL event order: %#v", messages)
	}
}

func TestSubmitReturnsUnmatchedWhenNoActiveWaiter(t *testing.T) {
	fixture := newTestFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/submit", bytes.NewBufferString(`{"runId":"missing-run","awaitingId":"missing-awaiting","params":[{"id":"q1","answer":"ok"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown awaitingId") {
		t.Fatalf("expected unknown awaitingId error, got %s", rec.Body.String())
	}
}

func mustEncodeSubmitParams(t *testing.T, value any) api.SubmitParams {
	t.Helper()
	params, err := api.EncodeSubmitParams(value)
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
				{"decision": "approve_prefix_run"},
				{"decision": "reject"},
			}),
		},
		{
			name:      "form",
			mode:      "form",
			itemCount: 1,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"action": "submit", "form": map[string]any{"days": 2}},
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
		{
			name:      "form",
			mode:      "form",
			itemCount: 1,
			params: mustEncodeSubmitParams(t, []map[string]any{
				{"id": "wrong-form", "action": "cancel"},
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
		{
			name:       "form missing action",
			mode:       "form",
			item:       map[string]any{"form": map[string]any{"days": 2}},
			wantSubstr: "items[0]: form items require action",
		},
		{
			name:       "form invalid action",
			mode:       "form",
			item:       map[string]any{"action": "approve"},
			wantSubstr: `items[0]: unsupported form action "approve"`,
		},
		{
			name:       "form submit missing form",
			mode:       "form",
			item:       map[string]any{"action": "submit"},
			wantSubstr: "items[0]: submit action requires form",
		},
		{
			name:       "form field not object",
			mode:       "form",
			item:       map[string]any{"action": "submit", "form": "bad"},
			wantSubstr: "items[0]: form field must be an object",
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
