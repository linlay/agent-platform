package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/resultsubmit"
	"agent-platform/internal/tools"
)

const executeTestAgent = "exec-agent"

var executeTestSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"summary": map[string]any{"type": "string"},
		"report":  map[string]any{"type": "string"},
	},
	"required":             []any{"summary"},
	"additionalProperties": false,
}

type executeModelRequest struct {
	Messages   []map[string]any `json:"messages"`
	Tools      []map[string]any `json:"tools"`
	ToolChoice string           `json:"tool_choice"`
}

// newExecuteFixture adds an EXECUTOR agent, cloned from mock-agent, whose
// tools include result_submit, and registers the result_submit handler the
// way app assembly does.
func newExecuteFixture(t *testing.T, model func(call int32, request executeModelRequest, w http.ResponseWriter)) (testFixture, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		var request executeModelRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &request)
		model(calls.Add(1), request, w)
	}, testFixtureOptions{
		sandbox: &recordingSandbox{},
		setupRuntime: func(_ string, cfg *config.Config) {
			source, err := os.ReadFile(filepath.Join(cfg.Paths.AgentsDir, "mock-agent", "agent.yml"))
			if err != nil {
				t.Fatalf("read mock agent: %v", err)
			}
			content := strings.Replace(string(source), "key: mock-agent", "key: "+executeTestAgent, 1)
			content = strings.Replace(content, "mode: GENERAL", "mode: EXECUTOR", 1)
			content = strings.Replace(content, "    - bash\n", "    - bash\n    - result_submit\n", 1)
			if err := os.MkdirAll(filepath.Join(cfg.Paths.AgentsDir, executeTestAgent), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Paths.AgentsDir, executeTestAgent, "agent.yml"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	})
	router, ok := fixture.tools.(*tools.ToolRouter)
	if !ok {
		t.Fatalf("fixture tools are %T", fixture.tools)
	}
	if err := router.RegisterHandler(resultsubmit.NewToolHandler()); err != nil {
		t.Fatal(err)
	}
	return fixture, &calls
}

func postExecute(t *testing.T, server *Server, body map[string]any) (int, api.ApiResponse[map[string]any]) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/execute", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	return serveExecute(t, server, req)
}

func serveExecute(t *testing.T, server *Server, req *http.Request) (int, api.ApiResponse[map[string]any]) {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var response api.ApiResponse[map[string]any]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode execute response %d: %v: %s", rec.Code, err, rec.Body.String())
	}
	return rec.Code, response
}

func submitFrame(t *testing.T, id string, args map[string]any) string {
	return providerToolCallFrame(t, id, "result_submit", args)
}

func modelTool(request executeModelRequest, name string) map[string]any {
	for _, tool := range request.Tools {
		function, _ := tool["function"].(map[string]any)
		if function["name"] == name {
			return function
		}
	}
	return nil
}

func messageText(message map[string]any) string {
	switch content := message["content"].(type) {
	case string:
		return content
	default:
		data, _ := json.Marshal(content)
		return string(data)
	}
}

func lastMessage(request executeModelRequest) map[string]any {
	if len(request.Messages) == 0 {
		return nil
	}
	return request.Messages[len(request.Messages)-1]
}

func executeErrorCode(response api.ApiResponse[map[string]any]) string {
	payload, _ := response.Data["error"].(map[string]any)
	code, _ := payload["code"].(string)
	return code
}

func TestExecuteDeliversAcceptedResult(t *testing.T) {
	var initialToolCount int
	var initialToolChoice string
	fixture, calls := newExecuteFixture(t, func(call int32, request executeModelRequest, w http.ResponseWriter) {
		if call == 2 {
			if len(request.Tools) != initialToolCount || request.ToolChoice != initialToolChoice {
				t.Errorf("submission changed normal tool availability: %#v", request)
			}
			last := lastMessage(request)
			if last["role"] != "tool" || !strings.Contains(messageText(last), `"accepted":true`) {
				t.Errorf("expected the ordinary accepted tool result, got %#v", last)
			}
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"审查完成，未发现风险。"},"finish_reason":"stop"}]}`, `[DONE]`)
			return
		}
		if call != 1 {
			t.Fatalf("unexpected model call %d", call)
		}
		initialToolCount, initialToolChoice = len(request.Tools), request.ToolChoice
		tool := modelTool(request, "result_submit")
		parameters, _ := tool["parameters"].(map[string]any)
		properties, _ := parameters["properties"].(map[string]any)
		output, _ := properties["output"].(map[string]any)
		outputProperties, _ := output["properties"].(map[string]any)
		if _, ok := outputProperties["summary"]; !ok {
			t.Errorf("result_submit does not carry the caller's output format: %#v", tool)
		}
		user := ""
		for _, message := range request.Messages {
			if message["role"] == "user" {
				user = messageText(message)
			}
		}
		if !strings.Contains(user, "审查合同") || !strings.Contains(user, "[Task input]") || !strings.Contains(user, `"customer":"VIP"`) {
			t.Errorf("model input lacks the task message or data: %s", user)
		}
		writeProviderSSE(t, w, submitFrame(t, "call_submit", map[string]any{"status": "success", "output": map[string]any{"summary": "无风险"}}), `[DONE]`)
	})
	status, response := postExecute(t, fixture.server, map[string]any{
		"agentKey":     executeTestAgent,
		"message":      "审查合同",
		"input":        map[string]any{"customer": "VIP"},
		"outputSchema": executeTestSchema,
	})
	if status != http.StatusOK || response.Data["status"] != "success" {
		t.Fatalf("execute: %d %#v", status, response)
	}
	output, _ := response.Data["output"].(map[string]any)
	if output["summary"] != "无风险" || response.Data["chatId"] == "" || calls.Load() != 2 {
		t.Fatalf("unexpected result %#v after %d model calls", response.Data, calls.Load())
	}
	chatID, _ := response.Data["chatId"].(string)
	messages, err := fixture.chats.LoadRawMessages(chatID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range messages {
		if message["role"] == "assistant" && strings.Contains(messageText(message), "审查完成，未发现风险。") {
			found = true
		}
	}
	if !found {
		t.Fatalf("final assistant response missing from Chat: %#v", messages)
	}
}

func TestExecuteReturnsDeclaredFailure(t *testing.T) {
	fixture, calls := newExecuteFixture(t, func(call int32, _ executeModelRequest, w http.ResponseWriter) {
		if call == 2 {
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"缺少合同第 3 页，暂时无法完成审查。"},"finish_reason":"stop"}]}`, `[DONE]`)
			return
		}
		writeProviderSSE(t, w, submitFrame(t, "call_submit", map[string]any{"status": "failure", "reason": "缺少合同第 3 页"}), `[DONE]`)
	})
	status, response := postExecute(t, fixture.server, map[string]any{"agentKey": executeTestAgent, "message": "审查合同", "outputSchema": executeTestSchema})
	if status != http.StatusOK || response.Data["status"] != "failure" || response.Data["reason"] != "缺少合同第 3 页" || calls.Load() != 2 {
		t.Fatalf("execute: %d %#v", status, response)
	}
	if _, ok := response.Data["output"]; ok {
		t.Fatalf("failure carries output: %#v", response.Data)
	}
}

func TestQueryResultSubmitContinuesUntilModelFinishes(t *testing.T) {
	fixture, calls := newExecuteFixture(t, func(call int32, request executeModelRequest, w http.ResponseWriter) {
		switch call {
		case 1:
			writeProviderSSE(t, w, submitFrame(t, "call_submit", map[string]any{"status": "success", "output": map[string]any{"summary": "ok"}}), `[DONE]`)
		case 2:
			if lastMessage(request)["role"] != "tool" {
				t.Errorf("expected normal tool continuation: %#v", lastMessage(request))
			}
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"任务已完成，结果已提交。"},"finish_reason":"stop"}]}`, `[DONE]`)
		default:
			t.Fatalf("unexpected model call %d", call)
		}
	})
	body, err := json.Marshal(api.QueryRequest{ChatID: "submit-chat", AgentKey: executeTestAgent, Message: "总结"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query?detail=full", bytes.NewReader(body)))
	if rec.Code != http.StatusOK || calls.Load() != 2 || !strings.Contains(rec.Body.String(), "任务已完成，结果已提交。") || !strings.Contains(rec.Body.String(), `"type":"run.complete"`) {
		t.Fatalf("query did not finish normally after submission: %d, %d model calls, %s", rec.Code, calls.Load(), rec.Body.String())
	}
}

func TestExecuteSendsRejectedSubmissionBackToTheModel(t *testing.T) {
	fixture, calls := newExecuteFixture(t, func(call int32, request executeModelRequest, w http.ResponseWriter) {
		switch call {
		case 1:
			writeProviderSSE(t, w, submitFrame(t, "call_bad", map[string]any{"status": "success", "output": map[string]any{"summary": 5}}), `[DONE]`)
		case 2:
			if text := messageText(lastMessage(request)); !strings.Contains(text, resultsubmit.CodeInvalid) {
				t.Errorf("model did not see the rejection: %s", text)
			}
			writeProviderSSE(t, w, submitFrame(t, "call_good", map[string]any{"status": "success", "output": map[string]any{"summary": "ok"}}), `[DONE]`)
		case 3:
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"已完成。"},"finish_reason":"stop"}]}`, `[DONE]`)
		default:
			t.Fatalf("unexpected model call %d", call)
		}
	})
	status, response := postExecute(t, fixture.server, map[string]any{"agentKey": executeTestAgent, "message": "总结", "outputSchema": executeTestSchema})
	if status != http.StatusOK || response.Data["status"] != "success" || calls.Load() != 3 {
		t.Fatalf("execute: %d %#v after %d calls", status, response, calls.Load())
	}
}

func TestExecuteFailsAfterRepeatedRejections(t *testing.T) {
	fixture, calls := newExecuteFixture(t, func(call int32, _ executeModelRequest, w http.ResponseWriter) {
		writeProviderSSE(t, w, submitFrame(t, "call_bad", map[string]any{"status": "success", "output": map[string]any{}}), `[DONE]`)
	})
	status, response := postExecute(t, fixture.server, map[string]any{"agentKey": executeTestAgent, "message": "总结", "outputSchema": executeTestSchema})
	if status != http.StatusUnprocessableEntity || executeErrorCode(response) != "output_invalid" || response.Data["chatId"] == "" {
		t.Fatalf("execute: %d %#v", status, response)
	}
	if calls.Load() != resultsubmit.MaxInvalidSubmissions {
		t.Fatalf("model calls = %d, want %d", calls.Load(), resultsubmit.MaxInvalidSubmissions)
	}
}

func TestExecuteRemindsOnceThenFailsWithoutResult(t *testing.T) {
	fixture, calls := newExecuteFixture(t, func(call int32, request executeModelRequest, w http.ResponseWriter) {
		if call == 2 && !strings.Contains(messageText(lastMessage(request)), "result_submit") {
			t.Errorf("second model call lacks the reminder: %#v", lastMessage(request))
		}
		writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"看起来没问题"},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	status, response := postExecute(t, fixture.server, map[string]any{"agentKey": executeTestAgent, "message": "总结", "outputSchema": executeTestSchema})
	if status != http.StatusUnprocessableEntity || executeErrorCode(response) != "output_missing" || calls.Load() != 2 {
		t.Fatalf("execute: %d %#v after %d calls", status, response, calls.Load())
	}
}

func TestExecuteFailsInsteadOfWaitingForApproval(t *testing.T) {
	fixture, calls := newExecuteFixture(t, func(call int32, _ executeModelRequest, w http.ResponseWriter) {
		if call != 1 {
			t.Fatalf("the Run continued after an approval was needed: call %d", call)
		}
		writeProviderSSE(t, w, providerToolCallFrame(t, "tool_bash", "bash", map[string]any{
			"command":     "rm -r old-images",
			"description": "删除旧镜像目录",
			"cwd":         "@chat",
		}), `[DONE]`)
	})
	status, response := postExecute(t, fixture.server, map[string]any{"agentKey": executeTestAgent, "message": "清理镜像", "outputSchema": executeTestSchema})
	if status != http.StatusForbidden || executeErrorCode(response) != "approval_required" || calls.Load() != 1 {
		t.Fatalf("execute: %d %#v after %d calls", status, response, calls.Load())
	}
}

func TestExecuteRequiresResultSubmitTool(t *testing.T) {
	fixture, calls := newExecuteFixture(t, func(call int32, _ executeModelRequest, _ http.ResponseWriter) {
		t.Fatalf("model called for an agent without result_submit")
	})
	status, response := postExecute(t, fixture.server, map[string]any{"agentKey": "mock-agent", "message": "总结", "outputSchema": executeTestSchema})
	if status != http.StatusBadRequest || executeErrorCode(response) != "result_submit_unavailable" || calls.Load() != 0 {
		t.Fatalf("execute: %d %#v", status, response)
	}
}

func TestExecuteRejectsInvalidRequestsBeforeStarting(t *testing.T) {
	fixture, calls := newExecuteFixture(t, func(_ int32, _ executeModelRequest, _ http.ResponseWriter) {
		t.Fatalf("model called for an invalid request")
	})
	for name, body := range map[string]map[string]any{
		"array root":    {"agentKey": executeTestAgent, "message": "x", "outputSchema": map[string]any{"type": "array"}},
		"no schema":     {"agentKey": executeTestAgent, "message": "x"},
		"no message":    {"agentKey": executeTestAgent, "outputSchema": executeTestSchema},
		"unknown field": {"agentKey": executeTestAgent, "message": "x", "outputSchema": executeTestSchema, "chatId": "c1"},
		"case variant":  {"agentKey": executeTestAgent, "Message": "x", "outputSchema": executeTestSchema},
		"missing part":  {"agentKey": executeTestAgent, "message": "x", "outputSchema": executeTestSchema, "input": map[string]any{"doc": map[string]any{"$file": "doc"}}},
		"bad access":    {"agentKey": executeTestAgent, "message": "x", "outputSchema": executeTestSchema, "accessLevel": "root"},
		"unknown agent": {"agentKey": "nobody", "message": "x", "outputSchema": executeTestSchema},
	} {
		status, response := postExecute(t, fixture.server, body)
		if status != http.StatusBadRequest && status != http.StatusNotFound {
			t.Errorf("%s: %d %#v", name, status, response)
		}
		if _, ok := response.Data["chatId"]; ok {
			t.Errorf("%s created a Chat: %#v", name, response.Data)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("model calls = %d", calls.Load())
	}
}

func TestExecuteStoresInputFiles(t *testing.T) {
	fixture, calls := newExecuteFixture(t, func(call int32, request executeModelRequest, w http.ResponseWriter) {
		if call == 2 {
			writeProviderSSE(t, w, `{"choices":[{"delta":{"content":"审查完成。"},"finish_reason":"stop"}]}`, `[DONE]`)
			return
		}
		if call != 1 {
			t.Fatalf("unexpected model call %d", call)
		}
		user := ""
		for _, message := range request.Messages {
			if message["role"] == "user" {
				user = messageText(message)
			}
		}
		if !strings.Contains(user, `"contract":"@chat/合同.txt"`) {
			t.Errorf("input file reference was not replaced: %s", user)
		}
		// Output is the caller's structure; an @chat string is not interpreted.
		writeProviderSSE(t, w, submitFrame(t, "call_ok", map[string]any{"status": "success", "output": map[string]any{"summary": "ok", "report": "@chat/not-written.md"}}), `[DONE]`)
	})
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	request, _ := json.Marshal(map[string]any{
		"agentKey":     executeTestAgent,
		"message":      "审查合同",
		"input":        map[string]any{"contract": map[string]any{"$file": "contract"}},
		"outputSchema": executeTestSchema,
	})
	if err := writer.WriteField("request", string(request)); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("contract", "合同.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("第一条"))
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/execute", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	status, response := serveExecute(t, fixture.server, req)
	if status != http.StatusOK || response.Data["status"] != "success" || calls.Load() != 2 {
		t.Fatalf("execute: %d %#v after %d calls", status, response, calls.Load())
	}
	chatID, _ := response.Data["chatId"].(string)
	data, err := os.ReadFile(filepath.Join(fixture.chats.ChatDir(chatID), "合同.txt"))
	if err != nil || string(data) != "第一条" {
		t.Fatalf("input file not stored in the Chat: %q %v", data, err)
	}
}

func TestExecuteRejectsUnusedFileParts(t *testing.T) {
	fixture, calls := newExecuteFixture(t, func(_ int32, _ executeModelRequest, _ http.ResponseWriter) {
		t.Fatalf("model called for an invalid request")
	})
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	request, _ := json.Marshal(map[string]any{"agentKey": executeTestAgent, "message": "x", "outputSchema": executeTestSchema})
	_ = writer.WriteField("request", string(request))
	part, _ := writer.CreateFormFile("extra", "extra.txt")
	_, _ = part.Write([]byte("x"))
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/execute", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	status, response := serveExecute(t, fixture.server, req)
	if status != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("execute: %d %#v", status, response)
	}
}
