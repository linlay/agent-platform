package server

import (
	"context"
	"io"
	"testing"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/llm"
)

type orchestratorRegistry struct {
	testCatalogRegistry
	agents map[string]catalog.AgentDefinition
}

func (r orchestratorRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	def, ok := r.agents[key]
	return def, ok
}

type orchestratorAgentEngine struct {
	stream contracts.AgentStream
	err    error
}

func (e orchestratorAgentEngine) Stream(context.Context, api.QueryRequest, contracts.QuerySession) (contracts.AgentStream, error) {
	if e.err != nil {
		return nil, e.err
	}
	return e.stream, nil
}

type injectedToolResult struct {
	toolID  string
	text    string
	isError bool
}

type stubOrchestratableStream struct {
	deltas    []contracts.AgentDelta
	index     int
	injected  []injectedToolResult
	finalText string
}

func (s *stubOrchestratableStream) Next() (contracts.AgentDelta, error) {
	if s.index >= len(s.deltas) {
		return nil, io.EOF
	}
	delta := s.deltas[s.index]
	s.index++
	return delta, nil
}

func (s *stubOrchestratableStream) Close() error { return nil }

func (s *stubOrchestratableStream) InjectToolResult(toolID string, text string, isError bool) bool {
	s.injected = append(s.injected, injectedToolResult{toolID: toolID, text: text, isError: isError})
	return true
}

func (s *stubOrchestratableStream) FinalAssistantContent() (string, bool) {
	if s.finalText == "" {
		return "", false
	}
	return s.finalText, true
}

var _ llm.OrchestratableAgentStream = (*stubOrchestratableStream)(nil)

func TestFrameOrchestratorRejectsInvalidSubAgentMode(t *testing.T) {
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			contracts.DeltaInvokeSubAgent{
				MainToolID:  "tool_main_1",
				SubAgentKey: "planner",
				TaskText:    "make a plan",
				TaskName:    "规划",
			},
		},
	}
	var emitted []contracts.AgentDelta
	orchestrator := &frameOrchestrator{
		runCtx:  context.Background(),
		request: api.QueryRequest{RunID: "run_1", ChatID: "chat_1"},
		session: contracts.QuerySession{RunID: "run_1", Mode: "REACT"},
		summary: chat.Summary{ChatID: "chat_1", ChatName: "demo"},
		registry: orchestratorRegistry{agents: map[string]catalog.AgentDefinition{
			"planner": {
				Key:  "planner",
				Mode: "PLAN_EXECUTE",
			},
		}},
		buildQuerySession: func(context.Context, api.QueryRequest, chat.Summary, catalog.AgentDefinition, querySessionBuildOptions) (contracts.QuerySession, error) {
			return contracts.QuerySession{}, nil
		},
		emitDelta: func(delta contracts.AgentDelta) { emitted = append(emitted, delta) },
	}

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}
	if len(emitted) != 0 {
		t.Fatalf("expected no task lifecycle deltas on invalid mode reject, got %#v", emitted)
	}
	if len(mainStream.injected) != 1 || !mainStream.injected[0].isError || mainStream.injected[0].text != "sub-agent must be REACT/ONESHOT" {
		t.Fatalf("expected error tool result injected into main stream, got %#v", mainStream.injected)
	}
}

func TestFrameOrchestratorRunsSubAgentAndCompletesTask(t *testing.T) {
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			contracts.DeltaInvokeSubAgent{
				MainToolID:  "tool_main_1",
				SubAgentKey: "writer",
				TaskText:    "write a summary",
				TaskName:    "写作",
			},
		},
	}
	subStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			contracts.DeltaContent{Text: "child output"},
		},
		finalText: "final child answer",
	}
	var emitted []contracts.AgentDelta
	orchestrator := &frameOrchestrator{
		runCtx:  context.Background(),
		request: api.QueryRequest{RunID: "run_1", ChatID: "chat_1", TeamID: "team_1"},
		session: contracts.QuerySession{RunID: "run_1", ChatID: "chat_1", Mode: "REACT"},
		summary: chat.Summary{ChatID: "chat_1", ChatName: "demo"},
		agent:   orchestratorAgentEngine{stream: subStream},
		registry: orchestratorRegistry{agents: map[string]catalog.AgentDefinition{
			"writer": {
				Key:  "writer",
				Name: "Writer",
				Mode: "REACT",
			},
		}},
		buildQuerySession: func(_ context.Context, req api.QueryRequest, _ chat.Summary, agentDef catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
			if req.Message != "write a summary" || req.AgentKey != "writer" || options.IncludeHistory || options.IncludeMemory || options.AllowInvokeAgent {
				t.Fatalf("unexpected sub-agent session build request: req=%#v options=%#v", req, options)
			}
			return contracts.QuerySession{
				RunID:    req.RunID,
				ChatID:   req.ChatID,
				AgentKey: agentDef.Key,
				Mode:     agentDef.Mode,
			}, nil
		},
		emitDelta: func(delta contracts.AgentDelta) { emitted = append(emitted, delta) },
	}

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}
	if len(mainStream.injected) != 1 || mainStream.injected[0].isError || mainStream.injected[0].text != "final child answer" {
		t.Fatalf("expected successful tool result injected into main stream, got %#v", mainStream.injected)
	}
	if len(emitted) < 3 {
		t.Fatalf("expected task start, child delta, and task complete, got %#v", emitted)
	}
	start, ok := emitted[0].(contracts.DeltaTaskLifecycle)
	if !ok || start.Kind != "start" || start.SubAgentKey != "writer" || start.MainToolID != "tool_main_1" || start.TaskName != "写作" {
		t.Fatalf("unexpected task.start delta %#v", emitted[0])
	}
	if _, ok := emitted[1].(contracts.DeltaContent); !ok {
		t.Fatalf("expected child content delta in middle, got %#v", emitted[1])
	}
	complete, ok := emitted[len(emitted)-1].(contracts.DeltaTaskLifecycle)
	if !ok || complete.Kind != "complete" || complete.Status != "completed" {
		t.Fatalf("unexpected task completion delta %#v", emitted[len(emitted)-1])
	}
}

func TestFrameOrchestratorRejectsNestedInvokeAgentTool(t *testing.T) {
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			contracts.DeltaInvokeSubAgent{
				MainToolID:  "tool_main_1",
				SubAgentKey: "writer",
				TaskText:    "write a summary",
			},
		},
	}
	orchestrator := &frameOrchestrator{
		runCtx:  context.Background(),
		request: api.QueryRequest{RunID: "run_1", ChatID: "chat_1"},
		session: contracts.QuerySession{RunID: "run_1", Mode: "REACT"},
		summary: chat.Summary{ChatID: "chat_1", ChatName: "demo"},
		registry: orchestratorRegistry{agents: map[string]catalog.AgentDefinition{
			"writer": {
				Key:   "writer",
				Mode:  "REACT",
				Tools: []string{contracts.InvokeAgentToolName},
			},
		}},
		buildQuerySession: func(context.Context, api.QueryRequest, chat.Summary, catalog.AgentDefinition, querySessionBuildOptions) (contracts.QuerySession, error) {
			return contracts.QuerySession{}, nil
		},
		emitDelta: func(delta contracts.AgentDelta) {},
	}

	if _, _, err := orchestrator.Run(mainStream); err != nil {
		t.Fatalf("unexpected orchestrator error: %v", err)
	}
	if len(mainStream.injected) != 1 || !mainStream.injected[0].isError || mainStream.injected[0].text != "nested sub-agent invocation is not allowed" {
		t.Fatalf("expected nested-invoke rejection, got %#v", mainStream.injected)
	}
}
