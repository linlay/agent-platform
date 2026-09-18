package llm

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type staticToolsProtocol struct {
	retryProtocolStub
	requests [][]openAIToolSpec
}

func (p *staticToolsProtocol) PrepareRequest(params protocolStreamParams) (preparedProviderRequest, error) {
	var snapshot []openAIToolSpec
	raw, _ := json.Marshal(params.toolSpecs)
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return preparedProviderRequest{}, err
	}
	p.requests = append(p.requests, snapshot)
	return p.retryProtocolStub.PrepareRequest(params)
}

func (p *staticToolsProtocol) ConsumeChunk(s *llmRunStream, event, chunk string) (bool, error) {
	if chunk == "ok" {
		return p.retryProtocolStub.ConsumeChunk(s, event, chunk)
	}
	call := &toolCallAccumulator{ID: chunk, Type: "function", FunctionName: "desktop_cdp"}
	call.Arguments.WriteString(chunk)
	s.currentTurn.toolCalls = map[int]*toolCallAccumulator{0: call}
	s.currentTurn.finishReason = "tool_calls"
	s.currentTurn.hasMeaningful = true
	return true, s.finishCurrentTurn()
}

func TestPageToolsUseOrdinaryLoopAndStaticSchema(t *testing.T) {
	for _, result := range []contracts.ToolExecutionResult{
		{Output: `{"revision":"v1","site":{"name":"Orders","description":"Read orders"},"sections":[{"section":"orders.read","title":"Read orders"}]}`},
		{ExitCode: -1, Output: `{"error":{"code":"stale_revision"}}`},
		{ExitCode: -1, Error: "desktop_cdp_client_rejected", Output: "Read the manual and correct the input"},
	} {
		p := &staticToolsProtocol{retryProtocolStub: retryProtocolStub{outcomes: []retryProtocolOutcome{
			{chunk: `{"method":"AWCP.getManual"}`},
			{chunk: `{"method":"AWCP.invoke","params":{"revision":"v1","action":"orders.read","args":{}}}`},
			{chunk: `{"method":"AWCP.invoke","params":{"revision":"v1","action":"orders.read","args":{"id":"corrected"}}}`},
			{chunk: "ok"},
		}}}
		s := newRetryTestStream(&p.retryProtocolStub, 0)
		s.protocol = p
		s.allowToolUse = true
		definition := backendToolDefinition("desktop_cdp")
		executor := &recordingToolExecutor{defs: []api.ToolDetailResponse{definition}, result: result}
		s.engine.tools = executor
		s.toolSpecs = []openAIToolSpec{{Type: "function", Function: openAIToolDefinition{Name: "desktop_cdp", Parameters: definition.Parameters}}}
		prepared, _ := p.retryProtocolStub.PrepareRequest(protocolStreamParams{})
		s.systemInitCacheKey = "react:main"
		s.systemInitCacheUsed = true
		s.session.SystemInitCache = map[string]contracts.SystemInitSnapshot{"react:main": {
			AgentKey: "test-page", Fingerprint: "sha256:test-page",
			SystemMessage: firstSystemMessageSnapshot(s.messages), Tools: openAIToolSpecsToAny(s.toolSpecs),
			Model: s.currentModelSnapshot(prepared), ToolChoice: effectiveTraceToolChoice(s.toolChoice, s.toolSpecs),
			RequestOptions: requestOptionsFromPreparedBody(prepared.RequestBody),
		}}
		if err := s.prepareNextTurn(); err != nil {
			t.Fatal(err)
		}
		for {
			_, err := s.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(executor.invocations) != 3 || len(p.requests) != 4 {
			t.Fatalf("page result changed loop: calls=%d turns=%d", len(executor.invocations), len(p.requests))
		}
		for _, request := range p.requests {
			if !reflect.DeepEqual(request, p.requests[0]) {
				t.Fatal("page result changed model tool schema")
			}
		}
	}
}
