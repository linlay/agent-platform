package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func awcpTrustedPreflightResult(reason string) contracts.ToolExecutionResult {
	return contracts.ToolExecutionResult{Error: "desktop_cdp_client_rejected", ExitCode: -1,
		Structured: map[string]any{"details": map[string]any{
			"clientErrorType": "awcp_preflight_rejected", "stage": "desktop_preflight", "executionStarted": false, "reason": reason,
		}},
	}
}

// Exercises the actual multi-turn stream and queue, not just the observers.
// The model is scripted: this proves deterministic recovery, not model quality.
func TestAwcpRecoveryStreamAllowsIndependentCallsAndCorrectsAwcp(t *testing.T) {
	call := func(id, method, args string) openAIToolCall {
		return openAIToolCall{ID: id, Type: "function", Function: openAIFunctionCall{Name: method, Arguments: args}}
	}
	protocol := &awcpScriptedProtocol{
		chunks: []string{"malformed", "independent", "fixed-discovery", "bad-input", "fixed-input", "success-final"},
		turns: map[string][]openAIToolCall{
			"malformed": {call("malformed", desktopCdpToolName, `{"method":"AWCP.getSnapshot","params":{},"targetId":"desktop-f69b015e8acf46cf"}`)},
			"independent": {
				call("independent", desktopCdpToolName, `{"method":"Runtime.evaluate","params":{"expression":"1+1"}}`),
			},
			"fixed-discovery": {call("discovery", desktopCdpToolName, `{"method":"AWCP.getSnapshot","params":{}}`)},
			"bad-input":       {call("bad-input", desktopCdpToolName, `{"method":"AWCP.invoke","params":{"action":{"orders.read":{"unknown":true}}}}`)},
			"fixed-input":     {call("fixed-input", desktopCdpToolName, `{"method":"AWCP.invoke","params":{"action":{"orders.read":{}}}}`)},
		},
	}
	executor := &awcpScriptedExecutor{results: []contracts.ToolExecutionResult{
		{Output: "2"},
		awcpDiscoveryResult("revision-7", validAwcpTestActions()),
		awcpTrustedPreflightResult("input_schema_mismatch"),
		{Structured: map[string]any{"response": map[string]any{"ok": true}}, Output: `{"ok":true}`},
	}}
	s := &llmRunStream{
		engine: &LLMAgentEngine{tools: executor}, protocol: protocol, ctx: context.Background(),
		session:   contracts.QuerySession{RunID: "run-recovery", ChatID: "chat-recovery", AgentKey: "agent-awcp", Mode: "react"},
		model:     models.ModelDefinition{Key: "mock-model", ModelID: "mock-model-id", Protocol: "OPENAI"},
		provider:  models.ProviderDefinition{Key: "mock-provider"},
		messages:  []openAIMessage{{Role: "system", Content: "test system"}, {Role: "user", Content: "read orders"}},
		toolSpecs: cloneOpenAIToolSpecs(awcpTestStream().toolSpecs),
		execCtx:   &contracts.ExecutionContext{StartedAt: time.Now(), Budget: contracts.Budget{MaxSteps: 12}},
		maxSteps:  12, allowToolUse: true, systemInitCacheKey: "react:main", systemInitCacheUsed: true,
	}
	s.session.SystemInitCache = map[string]contracts.SystemInitSnapshot{"react:main": {
		AgentKey: "agent-awcp", Fingerprint: "sha256:test", SystemMessage: firstSystemMessageSnapshot(s.messages),
		Tools: openAIToolSpecsToAny(s.toolSpecs), ToolChoice: "auto",
		Model: map[string]any{"key": "mock-model", "id": "mock-model-id", "providerKey": "mock-provider", "protocol": "OPENAI", "endpoint": "https://provider.example.test/v1/chat/completions"},
	}}
	if err := s.prepareNextTurn(); err != nil {
		t.Fatal(err)
	}
	blocked := 0
	for {
		delta, err := s.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if result, ok := delta.(contracts.DeltaToolResult); ok && result.Result.Error == "awcp_recovery_required" {
			blocked++
		}
	}
	if blocked != 0 || len(executor.arguments) != 4 || protocol.openCount != 6 || s.awcpConstraint.stoppedReason != "" {
		t.Fatalf("recovery loop failed: blocked=%d executions=%#v turns=%d final=%q", blocked, executor.arguments, protocol.openCount, s.awcpConstraint.stoppedReason)
	}
	for i, method := range []string{"Runtime.evaluate", desktopAwcpSnapshotMethod, desktopAwcpInvokeMethod, desktopAwcpInvokeMethod} {
		if executor.arguments[i]["method"] != method {
			t.Fatalf("unexpected tool execution: %#v", executor.arguments)
		}
	}
	if s.awcpConstraint.totalRecoveries != 2 || s.awcpConstraint.pendingMethod != "" {
		t.Fatalf("wrong recovery state: %#v", s.awcpConstraint)
	}
	for _, request := range protocol.requests {
		if len(request.toolSpecs) == 0 || request.toolChoice == "none" {
			t.Fatal("recoverable rejection disabled tools")
		}
	}
}

func awcpPreparationTestStream() *llmRunStream {
	s := awcpTestStream()
	s.ctx = context.Background()
	s.engine = &LLMAgentEngine{cfg: config.Config{RuntimeMode: config.RuntimeModeDesktop}, tools: &awcpScriptedExecutor{}}
	s.execCtx = &contracts.ExecutionContext{}
	return s
}

func TestAwcpStateDoesNotSerializeIndependentTools(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		s := awcpPreparationTestStream()
		s.noteAwcpArgumentRejection(desktopCdpToolName, map[string]any{"method": desktopAwcpSnapshotMethod})
		if stopped {
			s.stopAwcp("correction_limit")
		}
		batch := []*preparedToolInvocation{
			{toolName: "datetime", args: map[string]any{}},
			{toolName: "regex", args: map[string]any{}},
		}
		if !s.canInvokeQueuedToolCallsConcurrently(batch) {
			t.Fatalf("AWCP state serialized independent tools: stopped=%v", stopped)
		}
		batch = append(batch, awcpDiscoveryInvocation())
		if s.canInvokeQueuedToolCallsConcurrently(batch) {
			t.Fatal("actual AWCP batch lost its ordering barrier")
		}
	}
}

func awcpFormatBatchTestStream() *llmRunStream {
	s := awcpPreparationTestStream()
	input := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	s.applyAwcpConstraint("revision-7", []awcpActionConstraint{
		{action: "orders.read", description: "Read orders", inputSchema: input},
		{action: "orders.summary", description: "Read summary", inputSchema: input},
	})
	_, s.awcpRequest, _ = s.awcpRequestTools()
	s.runLLMChatCompletionCount = 3
	return s
}

func awcpFormatBatchCall(id, action string, input any) openAIToolCall {
	arguments, _ := json.Marshal(map[string]any{
		"method": desktopAwcpInvokeMethod,
		"params": map[string]any{"action": map[string]any{action: input}},
	})
	return openAIToolCall{ID: id, Type: "function", Function: openAIFunctionCall{
		Name:      desktopCdpToolName,
		Arguments: string(arguments),
	}}
}

func TestAwcpValidBatchDoesNotEnterRecovery(t *testing.T) {
	s := awcpFormatBatchTestStream()
	var prepared []*preparedToolInvocation
	for _, action := range []string{"orders.read", "orders.summary"} {
		input := map[string]any{}
		if action == "orders.read" {
			input = map[string]any{"bad": true}
		}
		invocation, _, message := s.prepareToolCall(awcpFormatBatchCall(action, action, input))
		if invocation == nil || message != nil {
			t.Fatalf("valid batch member %s was rejected", action)
		}
		prepared = append(prepared, invocation)
	}
	if s.awcpConstraint.pendingMethod != "" || s.awcpConstraint.totalRecoveries != 0 {
		t.Fatal("preparation incorrectly created a recovery episode")
	}
	for _, invocation := range prepared {
		if err := s.awcpInvocationRecoveryGate(invocation); err != nil {
			t.Fatalf("valid batch could not execute: %v", err)
		}
	}
	// The first actual failure, not the last prepared call, owns recovery.
	s.observeDesktopToolResult(prepared[0], awcpTrustedPreflightResult("input_schema_mismatch"))
	success := contracts.ToolExecutionResult{Structured: map[string]any{"response": map[string]any{"ok": true}}}
	s.observeDesktopToolResult(prepared[1], success)
	if s.awcpConstraint.pendingAction != "orders.read" || s.awcpConstraint.inputRecoveries != 1 {
		t.Fatal("sibling success cleared or replaced the failed Action")
	}
	s.runLLMChatCompletionCount++
	corrected, _, _ := s.prepareToolCall(awcpFormatBatchCall("corrected", "orders.read", map[string]any{}))
	if corrected == nil {
		t.Fatal("next response could not correct the rejected Action")
	}
	s.observeDesktopToolResult(corrected, success)
	if s.awcpConstraint.pendingMethod != "" || s.awcpConstraint.totalRecoveries != 1 {
		t.Fatal("successful correction did not end recovery")
	}
}

func TestAwcpMalformedJSONUsesTheResponseCorrectionBudget(t *testing.T) {
	s := awcpFormatBatchTestStream()
	call := openAIToolCall{ID: "partial", Function: openAIFunctionCall{Name: desktopCdpToolName, Arguments: `{"method":"AWCP.invoke"`}}
	for i := 0; i < 2; i++ {
		invocation, _, message := s.prepareToolCall(call)
		if invocation != nil || message == nil {
			t.Fatal("malformed JSON was repaired or lost its error")
		}
	}
	if s.awcpConstraint.formatRecoveries != 1 || s.awcpConstraint.stoppedReason != "" {
		t.Fatal("same-response malformed JSON exhausted recovery")
	}
	s.runLLMChatCompletionCount++
	s.prepareToolCall(call)
	if s.awcpConstraint.formatRecoveries != 2 || s.awcpConstraint.stoppedReason == "" {
		t.Fatal("repeated malformed JSON was not bounded")
	}
	ordinary := awcpFormatBatchTestStream()
	ordinary.noteAwcpMalformedArguments(desktopCdpToolName, `{"method":"Runtime.evaluate"`)
	if ordinary.awcpConstraint.pendingMethod != "" {
		t.Fatal("ordinary CDP entered AWCP recovery")
	}
}

func TestAwcpStopLeavesOrdinaryToolsAndErrorsUnchanged(t *testing.T) {
	s := awcpFormatBatchTestStream()
	s.stopAwcp("correction_limit")
	for _, method := range []string{desktopAwcpSnapshotMethod, desktopAwcpInvokeMethod} {
		if err := s.awcpRecoveryGate(desktopCdpToolName, map[string]any{"method": method}); err == nil {
			t.Fatalf("stopped Run allowed %s", method)
		}
	}
	for _, tool := range []string{"file_read", "bash", "ask_user", desktopCdpToolName} {
		if err := s.awcpRecoveryGate(tool, map[string]any{"method": "Runtime.evaluate"}); err != nil {
			t.Fatalf("AWCP stop affected %s: %v", tool, err)
		}
	}
	_, _, message := s.prepareToolCall(openAIToolCall{ID: "ordinary", Function: openAIFunctionCall{Name: "file_read", Arguments: "{"}})
	if message == nil || message.Content != "invalid tool arguments: unexpected end of JSON input" {
		t.Fatalf("ordinary tool error changed: %#v", message)
	}
	before := s.awcpConstraint.totalRecoveries
	_, _, message = s.prepareToolCall(openAIToolCall{ID: "rediscover", Function: openAIFunctionCall{Name: desktopCdpToolName, Arguments: `{"method":"AWCP.getSnapshot"}`}})
	if message == nil || !strings.Contains(message.Content.(string), "correction_limit") || s.awcpConstraint.totalRecoveries != before {
		t.Fatal("rediscovery did not retain the original stop reason and budget")
	}
}

func TestAwcpSameResponseFormatErrorsLeaveOneCorrection(t *testing.T) {
	for _, correctionFails := range []bool{false, true} {
		s := awcpFormatBatchTestStream()
		for _, action := range []string{"orders.read", "orders.summary", "orders.read"} {
			invocation, events, message := s.prepareToolCall(awcpFormatBatchCall(action, action, ""))
			if invocation != nil || message == nil || len(events) != 1 || events[0].(contracts.DeltaToolResult).Result.Error != "invalid_tool_arguments" {
				t.Fatalf("lost an individual format error: %#v %#v", invocation, events)
			}
		}
		if s.awcpConstraint.formatRecoveries != 1 || s.awcpConstraint.totalRecoveries != 1 || s.awcpConstraint.pendingAction != "orders.read" || s.awcpConstraint.stoppedReason != "" || len(s.toolSpecs) == 0 {
			t.Fatalf("same-response failures exhausted or changed recovery: %#v", s.awcpConstraint)
		}
		s.runLLMChatCompletionCount++
		if correctionFails {
			for _, action := range []string{"orders.read", "orders.summary"} {
				s.prepareToolCall(awcpFormatBatchCall("retry-"+action, action, ""))
			}
			if s.awcpConstraint.formatRecoveries != 2 || s.awcpConstraint.totalRecoveries != 2 || s.awcpConstraint.stoppedReason == "" || len(s.toolSpecs) != 1 {
				t.Fatalf("failed correction was not bounded by response: %#v", s.awcpConstraint)
			}
			continue
		}
		invocation, _, _ := s.prepareToolCall(awcpFormatBatchCall("corrected", "orders.read", map[string]any{}))
		if invocation == nil || invocation.modelRunSeq != 4 {
			t.Fatal("next model response could not correct the first rejected Action")
		}
		s.observeDesktopToolResult(invocation, contracts.ToolExecutionResult{Structured: map[string]any{"response": map[string]any{"ok": true}}})
		if s.awcpConstraint.formatRecoveries != 0 || s.awcpConstraint.totalRecoveries != 1 || s.awcpConstraint.pendingMethod != "" {
			t.Fatalf("successful correction did not finish the episode: %#v", s.awcpConstraint)
		}
		if next, _, _ := s.prepareToolCall(awcpFormatBatchCall("continue", "orders.summary", map[string]any{})); next == nil {
			t.Fatal("successful correction prevented the remaining task")
		}
	}
}

func TestAwcpSameResponseSuccessCannotClearFormatRecovery(t *testing.T) {
	s := awcpFormatBatchTestStream()
	preparedSibling, _, _ := s.prepareToolCall(awcpFormatBatchCall("prepared-sibling", "orders.summary", map[string]any{}))
	if preparedSibling == nil {
		t.Fatal("valid sibling was rejected before the format error")
	}
	s.prepareToolCall(awcpFormatBatchCall("bad", "orders.read", ""))
	if err := s.awcpInvocationRecoveryGate(preparedSibling); err != nil {
		t.Fatalf("already prepared same-response sibling could not finish: %v", err)
	}
	invocation, _, _ := s.prepareToolCall(awcpFormatBatchCall("same-response", "orders.read", map[string]any{}))
	if invocation == nil {
		t.Fatal("well-formed same-Action call was rejected")
	}
	success := contracts.ToolExecutionResult{Structured: map[string]any{"response": map[string]any{"ok": true}}}
	s.observeDesktopToolResult(preparedSibling, success)
	s.observeDesktopToolResult(invocation, success)
	if s.awcpConstraint.formatRecoveries != 1 || s.awcpConstraint.pendingAction != "orders.read" {
		t.Fatal("same-response success erased an error the model has not seen")
	}
	other := map[string]any{"method": desktopAwcpInvokeMethod, "params": map[string]any{"action": map[string]any{"orders.summary": map[string]any{}}}}
	s.prepareToolCall(awcpFormatBatchCall("substitution", "orders.summary", map[string]any{}))
	if s.awcpConstraint.pendingAction != "orders.read" || s.awcpRecoveryGate(desktopCdpToolName, other) == nil {
		t.Fatal("a sibling Action replaced the correction target")
	}
	s.runLLMChatCompletionCount++
	s.observeDesktopToolResult(invocation, success)
	if s.awcpConstraint.formatRecoveries != 1 {
		t.Fatal("a late old-response result cleared the current recovery")
	}
	corrected, _, _ := s.prepareToolCall(awcpFormatBatchCall("next-response", "orders.read", map[string]any{}))
	if corrected == nil {
		t.Fatal("corrected Action was rejected")
	}
	s.observeDesktopToolResult(corrected, success)
	if s.awcpConstraint.formatRecoveries != 0 || s.awcpConstraint.totalRecoveries != 1 {
		t.Fatal("new-response success did not resolve recovery or lost the Run budget")
	}
}

func TestAwcpSameResponseDiscoveryPreservesSnapshotAndFormatRecovery(t *testing.T) {
	s := awcpFormatBatchTestStream()
	s.prepareToolCall(openAIToolCall{ID: "bad", Function: openAIFunctionCall{Name: desktopCdpToolName, Arguments: `{"method":"AWCP.getSnapshot","targetId":"not-allowed"}`}})
	call := openAIToolCall{ID: "snapshot", Function: openAIFunctionCall{Name: desktopCdpToolName, Arguments: `{"method":"AWCP.getSnapshot"}`}}
	invocation, _, _ := s.prepareToolCall(call)
	if invocation == nil {
		t.Fatal("valid snapshot was rejected")
	}
	s.observeDesktopToolResult(invocation, awcpDiscoveryResult("revision-8", validAwcpTestActions()))
	if s.awcpConstraint.revision != "revision-8" || s.awcpConstraint.formatRecoveries != 1 || s.awcpConstraint.pendingMethod != desktopAwcpSnapshotMethod {
		t.Fatal("same-response discovery dropped the snapshot or resolved an unseen error")
	}
	s.runLLMChatCompletionCount++
	invocation, _, _ = s.prepareToolCall(call)
	if invocation == nil {
		t.Fatal("next-response discovery was rejected")
	}
	s.observeDesktopToolResult(invocation, awcpDiscoveryResult("revision-9", validAwcpTestActions()))
	if s.awcpConstraint.formatRecoveries != 0 || s.awcpConstraint.pendingMethod != "" || s.awcpConstraint.revision != "revision-9" {
		t.Fatal("new-response discovery did not finish recovery")
	}
}

func TestAwcpIncidentDoesNotBlockUnrelatedTools(t *testing.T) {
	s := awcpPreparationTestStream()
	prepare := func(name, args string) (*preparedToolInvocation, []contracts.AgentDelta, *openAIMessage) {
		return s.prepareToolCall(openAIToolCall{ID: "call", Type: "function", Function: openAIFunctionCall{Name: name, Arguments: args}})
	}
	invocation, events, msg := prepare(desktopCdpToolName, `{"method":"AWCP.getSnapshot","params":{},"targetId":"desktop-f69b015e8acf46cf"}`)
	if invocation != nil || len(events) != 1 || msg == nil || !strings.Contains(msg.Content.(string), "targetId") || s.awcpConstraint.stoppedReason != "" {
		t.Fatalf("first malformed discovery did not permit precise correction: invocation=%#v events=%#v state=%#v", invocation, events, s.awcpConstraint)
	}
	for _, attempt := range []struct{ name, args string }{
		{desktopCdpToolName, `{"method":"Runtime.evaluate","params":{"expression":"document.body.innerText"}}`},
		{"file_read", `{"file_path":"@skills/desktop-cdp/SKILL.md"}`},
	} {
		invocation, events, _ := prepare(attempt.name, attempt.args)
		if invocation == nil {
			t.Fatalf("AWCP recovery blocked an unrelated tool: %#v", events)
		}
	}
	s.runLLMChatCompletionCount++ // The model has now received the original error.
	invocation, _, _ = prepare(desktopCdpToolName, `{"method":"AWCP.getSnapshot","params":{}}`)
	if invocation == nil {
		t.Fatal("legal empty-params correction was rejected")
	}
	s.observeDesktopToolResult(invocation, awcpDiscoveryResult("revision-7", validAwcpTestActions()))
	if s.awcpConstraint.pendingMethod != "" || s.awcpConstraint.revision != "revision-7" {
		t.Fatalf("successful discovery did not resolve the gate: %#v", s.awcpConstraint)
	}
}

func TestAwcpRecoveryDoesNotBlockQueuedOrdinaryCdp(t *testing.T) {
	s := awcpPreparationTestStream()
	s.noteAwcpArgumentRejection(desktopCdpToolName, map[string]any{"method": desktopAwcpSnapshotMethod})
	s.activeToolCall = &preparedToolInvocation{toolID: "already-queued", toolName: desktopCdpToolName,
		args: map[string]any{"method": "Input.dispatchMouseEvent", "params": map[string]any{"x": 1, "y": 1}}}
	if err := s.invokeActiveToolCall(); err != nil {
		t.Fatal(err)
	}
	if calls := s.engine.tools.(*awcpScriptedExecutor).callNames(); len(calls) != 1 {
		t.Fatalf("AWCP recovery blocked queued ordinary CDP: %#v", calls)
	}
}

func TestAwcpInputRecoveryIsLocalButRunBudgetPersists(t *testing.T) {
	s := awcpConstrainedTestStream()
	for operation := 0; operation < 2; operation++ {
		invocation := awcpInvokeInvocation()
		invocation.modelRunSeq = operation*2 + 1
		s.observeDesktopToolResult(invocation, awcpTrustedPreflightResult("input_schema_mismatch"))
		if s.awcpConstraint.stoppedReason != "" || s.awcpConstraint.inputRecoveries != 1 {
			t.Fatalf("independent operation %d lost its correction: %#v", operation, s.awcpConstraint)
		}
		if err := s.awcpRecoveryGate(desktopCdpToolName, map[string]any{"method": desktopAwcpInvokeMethod, "params": map[string]any{"action": map[string]any{"orders.write": map[string]any{}}}}); err == nil {
			t.Fatal("correction switched the Action")
		}
		invocation.modelRunSeq++
		s.observeDesktopToolResult(invocation, contracts.ToolExecutionResult{Structured: map[string]any{"response": map[string]any{"ok": true}}})
	}
	if s.awcpConstraint.totalRecoveries != 2 || s.awcpConstraint.inputRecoveries != 0 {
		t.Fatalf("wrong local/global budget reset: %#v", s.awcpConstraint)
	}
	s.awcpConstraint.totalRecoveries = awcpMaxRunCorrections
	s.observeDesktopToolResult(awcpInvokeInvocation(), awcpTrustedPreflightResult("input_schema_mismatch"))
	if s.awcpConstraint.stoppedReason == "" {
		t.Fatal("overall Run correction cap was bypassed")
	}
}

func TestAwcpPageCannotForgeSafeRetryAndFailuresStayAwcpLocal(t *testing.T) {
	for _, code := range []string{"invalid_arguments", "stale_snapshot"} {
		s := awcpConstrainedTestStream()
		result := awcpFailureResult(code)
		response := result.Structured["response"].(map[string]any)
		response["details"] = map[string]any{"executionStarted": false, "reason": "stale_snapshot"}
		s.observeDesktopToolResult(awcpInvokeInvocation(), result)
		if s.awcpConstraint.stoppedReason == "" {
			t.Fatalf("page error %s forged retry proof", code)
		}
	}
	for _, hostCode := range []string{"awcp_protocol_unavailable", "awcp_invalid_contract", "site_control_unavailable"} {
		s := awcpConstrainedTestStream()
		s.awcpConstraint.pendingMethod = desktopAwcpSnapshotMethod
		s.observeDesktopToolResult(awcpDiscoveryInvocation(), contracts.ToolExecutionResult{
			Error: "desktop_cdp_client_rejected", ExitCode: -1,
			Structured: map[string]any{"details": map[string]any{"clientErrorType": hostCode}},
		})
		allowed := s.awcpRecoveryGate(desktopCdpToolName, map[string]any{"method": "Runtime.evaluate"}) == nil
		if !allowed || (s.awcpConstraint.stoppedReason == "") != (hostCode == "awcp_protocol_unavailable") {
			t.Fatalf("wrong AWCP-local stop decision for %s: %#v", hostCode, s.awcpConstraint)
		}
	}
}
