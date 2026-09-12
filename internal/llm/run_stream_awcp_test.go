package llm

import (
	"bufio"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

func TestAwcpDiscoveryTightensCurrentRunToolSchema(t *testing.T) {
	stream := awcpTestStream()
	conditionSchema := map[string]any{
		"oneOf": []any{
			map[string]any{
				"type":                 "object",
				"required":             []any{"field", "oper", "value"},
				"additionalProperties": false,
				"properties": map[string]any{
					"field": map[string]any{"type": "string"},
					"oper":  map[string]any{"type": "string"},
					"value": map[string]any{},
				},
			},
			map[string]any{
				"type":                 "object",
				"required":             []any{"join", "nodes"},
				"additionalProperties": false,
				"properties": map[string]any{
					"join":  map[string]any{"type": "string", "enum": []any{"and", "or"}},
					"nodes": map[string]any{"type": "array", "minItems": float64(1), "items": map[string]any{"type": "object"}},
				},
			},
		},
	}
	actions := []any{
		map[string]any{
			"action":      "orders.query",
			"description": "Query orders",
			"inputSchema": map[string]any{
				"type":                 "object",
				"required":             []any{"condition"},
				"additionalProperties": false,
				"properties":           map[string]any{"condition": conditionSchema},
			},
		},
		map[string]any{
			"action":      "orders.read",
			"description": "Read one order",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false},
		},
	}

	stream.observeDesktopToolResult(awcpDiscoveryInvocation("target-a"), awcpDiscoveryResult("revision-7", actions))

	if stream.awcpConstraint.targetID != "target-a" || stream.awcpConstraint.revision != "revision-7" {
		t.Fatalf("unexpected AWCP binding: %#v", stream.awcpConstraint)
	}
	parameters := stream.toolSpecs[0].Function.Parameters
	branches, ok := parameters["oneOf"].([]any)
	if !ok || len(branches) != 2 {
		t.Fatalf("dynamic parameters = %#v, want two oneOf branches", parameters)
	}
	first := anyMap(branches[0])
	properties := anyMap(first["properties"])
	if anyMap(properties["revision"])["const"] != "revision-7" || anyMap(properties["action"])["const"] != "orders.query" {
		t.Fatalf("dynamic discriminants = %#v", properties)
	}
	wantArgs := anyMap(actions[0])["inputSchema"]
	if !reflect.DeepEqual(properties["args"], wantArgs) {
		t.Fatalf("dynamic args schema = %#v, want %#v", properties["args"], wantArgs)
	}

	group := anyMap(anyMap(anyMap(properties["args"])["properties"])["condition"])
	groupBranches := group["oneOf"].([]any)
	groupProperties := anyMap(anyMap(groupBranches[1])["properties"])
	if anyMap(groupProperties["nodes"])["type"] != "array" {
		t.Fatalf("condition nodes schema was not preserved: %#v", groupProperties["nodes"])
	}
}

func TestAwcpInvalidSnapshotDoesNotConstrainRunOrAnotherRun(t *testing.T) {
	first := awcpTestStream()
	second := awcpTestStream()
	invalid := []any{map[string]any{
		"action":      "orders.query",
		"description": "Query orders",
		"inputSchema": []any{},
	}}

	first.observeDesktopToolResult(awcpDiscoveryInvocation("target-a"), awcpDiscoveryResult("revision-7", invalid))

	if first.awcpConstraint.revision != "" || first.toolSpecs[0].Function.Parameters["oneOf"] != nil {
		t.Fatalf("invalid snapshot constrained first run: %#v", first.toolSpecs[0].Function.Parameters)
	}
	if second.awcpConstraint.revision != "" || second.toolSpecs[0].Function.Parameters["oneOf"] != nil {
		t.Fatalf("first run polluted second run: %#v", second.toolSpecs[0].Function.Parameters)
	}
}

func TestAwcpConstraintClearsOnKnownTargetChanges(t *testing.T) {
	methods := []string{"Page.navigate", "Page.reload", "Page.bringToFront", "Target.closeTarget"}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			stream := awcpConstrainedTestStream()
			stream.invalidateAwcpForDesktopCdpCall(desktopCdpToolName, map[string]any{"method": method})
			if stream.awcpConstraint.revision != "" || stream.awcpConstraint.targetID != "" {
				t.Fatalf("constraint was not cleared: %#v", stream.awcpConstraint)
			}
			if stream.toolSpecs[0].Function.Parameters["oneOf"] != nil {
				t.Fatalf("dynamic schema was not restored: %#v", stream.toolSpecs[0].Function.Parameters)
			}
		})
	}
}

func TestAwcpFailureStopsFurtherToolsAndStaleAllowsOneRecovery(t *testing.T) {
	for _, code := range []string{"invalid_arguments", "action_not_found", "execution_failed", "action.failed"} {
		t.Run(code, func(t *testing.T) {
			stream := awcpConstrainedTestStream()
			stream.observeDesktopToolResult(awcpInvokeInvocation(), awcpFailureResult(code))
			if stream.forcedFinalAnswer == "" || len(stream.toolSpecs) != 0 || stream.toolChoice != "none" {
				t.Fatalf("non-stale failure did not force a tool-free final answer: %#v", stream)
			}
		})
	}

	t.Run("host error cannot masquerade as stale", func(t *testing.T) {
		stream := awcpConstrainedTestStream()
		result := awcpFailureResult("stale_snapshot")
		result.Error = "desktop_awcp_client_rejected"
		stream.observeDesktopToolResult(awcpInvokeInvocation(), result)
		if stream.forcedFinalAnswer == "" || stream.awcpConstraint.staleRecoveries != 0 {
			t.Fatalf("host failure incorrectly entered stale recovery: %#v", stream.awcpConstraint)
		}
	})

	t.Run("single stale recovery", func(t *testing.T) {
		stream := awcpConstrainedTestStream()
		stream.observeDesktopToolResult(awcpInvokeInvocation(), awcpFailureResult("stale_snapshot"))
		if stream.forcedFinalAnswer != "" || stream.awcpConstraint.staleRecoveries != 1 || stream.awcpConstraint.revision != "" {
			t.Fatalf("first stale did not permit exactly one rediscovery: %#v", stream.awcpConstraint)
		}

		stream.observeDesktopToolResult(awcpDiscoveryInvocation("target-a"), awcpDiscoveryResult("revision-8", validAwcpTestActions()))
		stream.observeDesktopToolResult(awcpInvokeInvocation(), awcpFailureResult("stale_snapshot"))
		if stream.forcedFinalAnswer == "" || len(stream.toolSpecs) != 0 || stream.awcpConstraint.staleRecoveries != 2 {
			t.Fatalf("second stale did not stop tools: %#v", stream.awcpConstraint)
		}
	})
}

func TestAwcpScriptedInvalidArgumentsExecutesOnlyOnce(t *testing.T) {
	protocol := &awcpScriptedProtocol{chunks: []string{"discovery", "invalid-awcp", "final"}}
	executor := &awcpScriptedExecutor{}
	stream := &llmRunStream{
		engine:   &LLMAgentEngine{tools: executor},
		protocol: protocol,
		ctx:      context.Background(),
		session:  contracts.QuerySession{RunID: "run-awcp", ChatID: "chat-awcp", AgentKey: "agent-awcp", Mode: "react"},
		model:    models.ModelDefinition{Key: "mock-model", ModelID: "mock-model-id", Protocol: "OPENAI"},
		provider: models.ProviderDefinition{Key: "mock-provider"},
		messages: []openAIMessage{
			{Role: "system", Content: "test system"},
			{Role: "user", Content: "query orders"},
		},
		toolSpecs: append(awcpTestStream().toolSpecs, openAIToolSpec{
			Type: "function",
			Function: openAIToolDefinition{
				Name:       desktopCdpToolName,
				Parameters: map[string]any{"type": "object"},
			},
		}),
		execCtx: &contracts.ExecutionContext{
			StartedAt: time.Now(),
			Budget:    contracts.Budget{MaxSteps: 8},
		},
		maxSteps:            8,
		allowToolUse:        true,
		systemInitCacheKey:  "react:main",
		systemInitCacheUsed: true,
	}
	stream.session.SystemInitCache = map[string]contracts.SystemInitSnapshot{
		"react:main": {
			AgentKey:      "agent-awcp",
			Fingerprint:   "sha256:test",
			SystemMessage: firstSystemMessageSnapshot(stream.messages),
			Tools:         openAIToolSpecsToAny(stream.toolSpecs),
			Model: map[string]any{
				"key":         "mock-model",
				"id":          "mock-model-id",
				"providerKey": "mock-provider",
				"protocol":    "OPENAI",
				"endpoint":    "https://provider.example.test/v1/chat/completions",
			},
			ToolChoice: "auto",
		},
	}
	if err := stream.prepareNextTurn(); err != nil {
		t.Fatalf("prepare initial turn: %v", err)
	}

	var content strings.Builder
	for {
		delta, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("stream next: %v", err)
		}
		if text, ok := delta.(contracts.DeltaContent); ok {
			content.WriteString(text.Text)
		}
	}

	if got := executor.callNames(); !reflect.DeepEqual(got, []string{desktopCdpToolName, desktopAwcpToolName}) {
		t.Fatalf("executed tools = %#v, want one discovery and one AWCP attempt", got)
	}
	if protocol.openCount != 3 || len(protocol.requests) != 3 {
		t.Fatalf("model calls = %d requests=%d, want discovery, action, final", protocol.openCount, len(protocol.requests))
	}
	secondAwcp := findAwcpToolSpec(protocol.requests[1].toolSpecs)
	if secondAwcp == nil || secondAwcp.Function.Parameters["oneOf"] == nil {
		t.Fatalf("second request did not contain dynamic AWCP schema: %#v", protocol.requests[1].toolSpecs)
	}
	if len(protocol.requests[2].toolSpecs) != 0 || protocol.requests[2].toolChoice != "none" {
		t.Fatalf("final request retained tools: %#v", protocol.requests[2])
	}
	if !strings.Contains(content.String(), "AWCP arguments were rejected") {
		t.Fatalf("unexpected final content %q", content.String())
	}
}

type awcpScriptedRequest struct {
	toolSpecs  []openAIToolSpec
	toolChoice string
}

type awcpScriptedProtocol struct {
	chunks    []string
	openCount int
	requests  []awcpScriptedRequest
}

func (p *awcpScriptedProtocol) PrepareRequest(params protocolStreamParams) (preparedProviderRequest, error) {
	p.requests = append(p.requests, awcpScriptedRequest{
		toolSpecs:  cloneOpenAIToolSpecs(params.toolSpecs),
		toolChoice: params.toolChoice,
	})
	return preparedProviderRequest{
		Endpoint:        "https://provider.example.test/v1/chat/completions",
		RequestBody:     map[string]any{"model": "mock-model"},
		RequestBodyJSON: []byte(`{"model":"mock-model"}`),
	}, nil
}

func (p *awcpScriptedProtocol) OpenStream(context.Context, protocolStreamParams, preparedProviderRequest) (*providerTurnStream, error) {
	if p.openCount >= len(p.chunks) {
		return nil, errors.New("unexpected extra model call")
	}
	chunk := p.chunks[p.openCount]
	p.openCount++
	body := io.NopCloser(strings.NewReader("data: " + chunk + "\n\n"))
	return &providerTurnStream{body: body, reader: bufio.NewReader(body)}, nil
}

func (p *awcpScriptedProtocol) ConsumeChunk(stream *llmRunStream, _ string, chunk string) (bool, error) {
	switch chunk {
	case "discovery":
		stream.currentTurn.toolCalls = scriptedToolCalls("call-discovery", desktopCdpToolName, `{"method":"Runtime.evaluate","targetId":"target-a","params":{"expression":"globalThis.awcp.snapshot()","returnByValue":true}}`)
		stream.currentTurn.finishReason = "tool_calls"
		stream.currentTurn.hasMeaningful = true
		return true, stream.finishCurrentTurn()
	case "invalid-awcp":
		stream.currentTurn.toolCalls = scriptedToolCalls("call-awcp", desktopAwcpToolName, `{"revision":"revision-7","action":"orders.query","args":{"nodes":{"item":[]}}}`)
		stream.currentTurn.finishReason = "tool_calls"
		stream.currentTurn.hasMeaningful = true
		return true, stream.finishCurrentTurn()
	case "final":
		stream.appendCompatContent("AWCP arguments were rejected; correct the condition structure before trying again.")
		stream.currentTurn.finishReason = "stop"
		return true, stream.finishCurrentTurn()
	default:
		return false, errors.New("unexpected scripted chunk")
	}
}

type awcpScriptedExecutor struct {
	mu    sync.Mutex
	calls []string
}

func (e *awcpScriptedExecutor) Definitions() []api.ToolDetailResponse {
	return []api.ToolDetailResponse{{
		Name:       desktopAwcpToolName,
		Parameters: cloneToolSchemaMap(awcpTestStream().toolSpecs[0].Function.Parameters),
	}}
}

func (e *awcpScriptedExecutor) Invoke(_ context.Context, name string, _ map[string]any, _ *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	e.mu.Lock()
	e.calls = append(e.calls, name)
	e.mu.Unlock()
	switch name {
	case desktopCdpToolName:
		return awcpDiscoveryResult("revision-7", []any{map[string]any{
			"action":      "orders.query",
			"description": "Query orders",
			"inputSchema": map[string]any{
				"type":                 "object",
				"required":             []any{"nodes"},
				"additionalProperties": false,
				"properties": map[string]any{
					"nodes": map[string]any{"type": "array", "minItems": float64(1)},
				},
			},
		}}), nil
	case desktopAwcpToolName:
		return awcpFailureResult("invalid_arguments"), nil
	default:
		return contracts.ToolExecutionResult{}, errors.New("unexpected tool")
	}
}

func (e *awcpScriptedExecutor) callNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func scriptedToolCalls(id, name, arguments string) map[int]*toolCallAccumulator {
	call := &toolCallAccumulator{ID: id, Type: "function", FunctionName: name}
	call.Arguments.WriteString(arguments)
	return map[int]*toolCallAccumulator{0: call}
}

func cloneOpenAIToolSpecs(specs []openAIToolSpec) []openAIToolSpec {
	cloned := make([]openAIToolSpec, len(specs))
	for index, spec := range specs {
		cloned[index] = spec
		cloned[index].Function.Parameters = cloneToolSchemaMap(spec.Function.Parameters)
	}
	return cloned
}

func findAwcpToolSpec(specs []openAIToolSpec) *openAIToolSpec {
	for index := range specs {
		if specs[index].Function.Name == desktopAwcpToolName {
			return &specs[index]
		}
	}
	return nil
}

func awcpTestStream() *llmRunStream {
	return &llmRunStream{toolSpecs: []openAIToolSpec{{
		Type: "function",
		Function: openAIToolDefinition{
			Name: desktopAwcpToolName,
			Parameters: map[string]any{
				"type":                 "object",
				"required":             []any{"revision", "action", "args"},
				"additionalProperties": false,
				"properties": map[string]any{
					"revision": map[string]any{"type": "string"},
					"action":   map[string]any{"type": "string"},
					"args":     map[string]any{"type": "object", "additionalProperties": true},
				},
			},
		},
	}}}
}

func awcpConstrainedTestStream() *llmRunStream {
	stream := awcpTestStream()
	stream.observeDesktopToolResult(awcpDiscoveryInvocation("target-a"), awcpDiscoveryResult("revision-7", validAwcpTestActions()))
	return stream
}

func validAwcpTestActions() []any {
	return []any{map[string]any{
		"action":      "orders.read",
		"description": "Read orders",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false},
	}}
}

func awcpDiscoveryInvocation(targetID string) *preparedToolInvocation {
	return &preparedToolInvocation{
		toolName: desktopCdpToolName,
		args: map[string]any{
			"method":   "Runtime.evaluate",
			"targetId": targetID,
		},
	}
}

func awcpInvokeInvocation() *preparedToolInvocation {
	return &preparedToolInvocation{toolName: desktopAwcpToolName}
}

func awcpDiscoveryResult(revision string, actions []any) contracts.ToolExecutionResult {
	return contracts.ToolExecutionResult{
		Structured: map[string]any{
			"response": map[string]any{
				"ok":     true,
				"method": "Runtime.evaluate",
				"result": map[string]any{
					"result": map[string]any{
						"type": "object",
						"value": map[string]any{
							"ok": true,
							"snapshot": map[string]any{
								"revision": revision,
								"actions":  actions,
							},
						},
					},
				},
			},
		},
		ExitCode: 0,
	}
}

func awcpFailureResult(code string) contracts.ToolExecutionResult {
	return contracts.ToolExecutionResult{
		Structured: map[string]any{
			"response": map[string]any{
				"ok": false,
				"error": map[string]any{
					"code":    code,
					"message": "failed",
				},
			},
		},
		ExitCode: -1,
	}
}
