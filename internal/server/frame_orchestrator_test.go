package server

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/frontendtools"
	"agent-platform-runner-go/internal/llm"
	"agent-platform-runner-go/internal/stream"
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
	streams           []contracts.AgentStream
	streamsByAgentKey map[string]contracts.AgentStream
	err               error
	index             int
	mu                sync.Mutex
}

func (e *orchestratorAgentEngine) Stream(_ context.Context, req api.QueryRequest, _ contracts.QuerySession) (contracts.AgentStream, error) {
	if e.err != nil {
		return nil, e.err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.streamsByAgentKey) > 0 {
		stream, ok := e.streamsByAgentKey[req.AgentKey]
		if !ok {
			return nil, io.EOF
		}
		delete(e.streamsByAgentKey, req.AgentKey)
		return stream, nil
	}
	if e.index >= len(e.streams) {
		return nil, io.EOF
	}
	stream := e.streams[e.index]
	e.index++
	return stream, nil
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

type blockingOrchestratableStream struct {
	ctx context.Context
}

func (s *blockingOrchestratableStream) Next() (contracts.AgentDelta, error) {
	<-s.ctx.Done()
	return nil, contracts.ErrRunInterrupted
}

func (s *blockingOrchestratableStream) Close() error { return nil }

func (s *blockingOrchestratableStream) InjectToolResult(toolID string, text string, isError bool) bool {
	return false
}

func (s *blockingOrchestratableStream) FinalAssistantContent() (string, bool) {
	return "", false
}

var _ llm.OrchestratableAgentStream = (*stubOrchestratableStream)(nil)
var _ llm.OrchestratableAgentStream = (*blockingOrchestratableStream)(nil)

func newInvokeAgentsDelta(tasks ...contracts.SubAgentTaskSpec) contracts.DeltaInvokeSubAgents {
	return contracts.DeltaInvokeSubAgents{
		MainToolID: "tool_main_1",
		GroupID:    "group_tool_main_1",
		Tasks:      tasks,
	}
}

func newTestFrameOrchestrator(agent contracts.AgentEngine, registry map[string]catalog.AgentDefinition, emitted *[]contracts.AgentDelta, routed *[]stream.StreamInput) *frameOrchestrator {
	return newTestFrameOrchestratorWithContext(context.Background(), agent, registry, emitted, routed)
}

func newTestFrameOrchestratorWithContext(runCtx context.Context, agent contracts.AgentEngine, registry map[string]catalog.AgentDefinition, emitted *[]contracts.AgentDelta, routed *[]stream.StreamInput) *frameOrchestrator {
	return &frameOrchestrator{
		runCtx:  runCtx,
		request: api.QueryRequest{RunID: "run_1", ChatID: "chat_1", TeamID: "team_1"},
		session: contracts.QuerySession{RunID: "run_1", ChatID: "chat_1", Mode: "REACT"},
		summary: chat.Summary{ChatID: "chat_1", ChatName: "demo"},
		agent:   agent,
		registry: orchestratorRegistry{
			agents: registry,
		},
		buildQuerySession: func(_ context.Context, req api.QueryRequest, _ chat.Summary, agentDef catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
			if options.IncludeHistory || options.IncludeMemory || options.AllowInvokeAgents {
				t := "unexpected sub-agent session build options"
				panic(t)
			}
			return contracts.QuerySession{
				RunID:    req.RunID,
				ChatID:   req.ChatID,
				AgentKey: agentDef.Key,
				Mode:     agentDef.Mode,
			}, nil
		},
		mapper: llm.NewDeltaMapper("run_1", "chat_1", 5000, nil, frontendtools.NewDefaultRegistry()),
		emitDelta: func(delta contracts.AgentDelta) {
			if emitted != nil {
				*emitted = append(*emitted, delta)
			}
		},
		emitInputs: func(inputs ...stream.StreamInput) {
			if routed != nil {
				*routed = append(*routed, inputs...)
			}
		},
	}
}

func TestFrameOrchestratorRejectsInvalidSubAgentMode(t *testing.T) {
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(contracts.SubAgentTaskSpec{
				SubAgentKey: "planner",
				TaskText:    "make a plan",
				TaskName:    "规划",
			}),
		},
	}
	var emitted []contracts.AgentDelta
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{}, map[string]catalog.AgentDefinition{
		"planner": {Key: "planner", Mode: "PLAN_EXECUTE"},
	}, &emitted, nil)

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

func TestFrameOrchestratorRunsBatchedSubAgentsAndAggregatesResult(t *testing.T) {
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(
				contracts.SubAgentTaskSpec{SubAgentKey: "writer", TaskText: "write a summary", TaskName: "写作"},
				contracts.SubAgentTaskSpec{SubAgentKey: "reviewer", TaskText: "review it", TaskName: "审查"},
			),
		},
	}
	childOne := &stubOrchestratableStream{
		deltas:    []contracts.AgentDelta{contracts.DeltaContent{Text: "child output"}},
		finalText: "final child answer",
	}
	childTwo := &stubOrchestratableStream{
		deltas:    []contracts.AgentDelta{contracts.DeltaReasoning{Text: "inspect"}},
		finalText: "reviewed",
	}
	var emitted []contracts.AgentDelta
	var routed []stream.StreamInput
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{streams: []contracts.AgentStream{childOne, childTwo}}, map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
	}, &emitted, &routed)

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}
	if len(mainStream.injected) != 1 || mainStream.injected[0].isError {
		t.Fatalf("expected successful aggregated tool result, got %#v", mainStream.injected)
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(mainStream.injected[0].text), &results); err != nil {
		t.Fatalf("expected JSON aggregate result: %v", err)
	}
	if len(results) != 2 || results[0]["taskName"] != "写作" || results[1]["taskName"] != "审查" {
		t.Fatalf("unexpected aggregated results %#v", results)
	}
	if len(emitted) != 4 {
		t.Fatalf("expected start/start/terminal/terminal lifecycle deltas, got %#v", emitted)
	}
	startOne, ok := emitted[0].(contracts.DeltaTaskLifecycle)
	if !ok || startOne.Kind != "start" || startOne.TaskName != "写作" || startOne.GroupID != "group_tool_main_1" {
		t.Fatalf("unexpected first task.start %#v", emitted[0])
	}
	startTwo, ok := emitted[1].(contracts.DeltaTaskLifecycle)
	if !ok || startTwo.Kind != "start" || startTwo.TaskName != "审查" {
		t.Fatalf("unexpected second task.start %#v", emitted[1])
	}
	if len(routed) == 0 {
		t.Fatal("expected child inputs to be routed through emitInputs")
	}
}

func TestFrameOrchestratorRejectsNestedInvokeAgentsTool(t *testing.T) {
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(contracts.SubAgentTaskSpec{
				SubAgentKey: "writer",
				TaskText:    "write a summary",
			}),
		},
	}
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{}, map[string]catalog.AgentDefinition{
		"writer": {
			Key:   "writer",
			Mode:  "REACT",
			Tools: []string{contracts.InvokeAgentsToolName},
		},
	}, nil, nil)

	if _, _, err := orchestrator.Run(mainStream); err != nil {
		t.Fatalf("unexpected orchestrator error: %v", err)
	}
	if len(mainStream.injected) != 1 || !mainStream.injected[0].isError || mainStream.injected[0].text != "nested sub-agent invocation is not allowed" {
		t.Fatalf("expected nested-invoke rejection, got %#v", mainStream.injected)
	}
}

func TestFrameOrchestratorPartialFailureAggregation(t *testing.T) {
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(
				contracts.SubAgentTaskSpec{SubAgentKey: "writer", TaskText: "write a summary", TaskName: "写作"},
				contracts.SubAgentTaskSpec{SubAgentKey: "reviewer", TaskText: "review it", TaskName: "审查"},
				contracts.SubAgentTaskSpec{SubAgentKey: "publisher", TaskText: "publish it", TaskName: "发布"},
			),
		},
	}
	childOne := &stubOrchestratableStream{finalText: "draft ready"}
	childTwo := &stubOrchestratableStream{}
	childThree := &stubOrchestratableStream{finalText: "published"}
	var emitted []contracts.AgentDelta
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{streamsByAgentKey: map[string]contracts.AgentStream{
		"writer":    childOne,
		"reviewer":  childTwo,
		"publisher": childThree,
	}}, map[string]catalog.AgentDefinition{
		"writer":    {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer":  {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
		"publisher": {Key: "publisher", Name: "Publisher", Mode: "REACT"},
	}, &emitted, nil)

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}
	if len(mainStream.injected) != 1 {
		t.Fatalf("expected one aggregated tool result, got %#v", mainStream.injected)
	}
	if !mainStream.injected[0].isError {
		t.Fatalf("expected partial failure to mark injected result as error, got %#v", mainStream.injected[0])
	}

	var results []childTaskResult
	if err := json.Unmarshal([]byte(mainStream.injected[0].text), &results); err != nil {
		t.Fatalf("expected JSON aggregate result: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected three aggregated results, got %#v", results)
	}
	if results[0].TaskName != "写作" || results[0].Status != "completed" || results[0].Text != "draft ready" {
		t.Fatalf("unexpected first aggregate result %#v", results[0])
	}
	if results[1].TaskName != "审查" || results[1].Status != "failed" || results[1].Error == "" {
		t.Fatalf("unexpected failed aggregate result %#v", results[1])
	}
	if results[2].TaskName != "发布" || results[2].Status != "completed" || results[2].Text != "published" {
		t.Fatalf("unexpected third aggregate result %#v", results[2])
	}

	if len(emitted) != 6 {
		t.Fatalf("expected three starts and three terminal lifecycle deltas, got %#v", emitted)
	}
	var failedLifecycle *contracts.DeltaTaskLifecycle
	for _, delta := range emitted[3:] {
		lifecycle, ok := delta.(contracts.DeltaTaskLifecycle)
		if !ok || lifecycle.Kind != "fail" {
			continue
		}
		failedLifecycle = &lifecycle
		break
	}
	if failedLifecycle == nil || failedLifecycle.Status != "failed" || failedLifecycle.Error == nil {
		t.Fatalf("expected one failed lifecycle delta in terminal events, got %#v", emitted)
	}
}

func TestFrameOrchestratorInterruptionMidExecution(t *testing.T) {
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(
				contracts.SubAgentTaskSpec{SubAgentKey: "writer", TaskText: "write a summary", TaskName: "写作"},
				contracts.SubAgentTaskSpec{SubAgentKey: "reviewer", TaskText: "review it", TaskName: "审查"},
				contracts.SubAgentTaskSpec{SubAgentKey: "publisher", TaskText: "publish it", TaskName: "发布"},
			),
		},
	}
	childOne := &blockingOrchestratableStream{ctx: runCtx}
	childTwo := &blockingOrchestratableStream{ctx: runCtx}
	childThree := &blockingOrchestratableStream{ctx: runCtx}
	var emitted []contracts.AgentDelta
	orchestrator := newTestFrameOrchestratorWithContext(runCtx, &orchestratorAgentEngine{streams: []contracts.AgentStream{childOne, childTwo, childThree}}, map[string]catalog.AgentDefinition{
		"writer":    {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer":  {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
		"publisher": {Key: "publisher", Name: "Publisher", Mode: "REACT"},
	}, &emitted, nil)

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}
	if len(mainStream.injected) != 1 || !mainStream.injected[0].isError {
		t.Fatalf("expected interrupted aggregate tool result marked as error, got %#v", mainStream.injected)
	}

	var results []childTaskResult
	if err := json.Unmarshal([]byte(mainStream.injected[0].text), &results); err != nil {
		t.Fatalf("expected JSON aggregate result: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected three aggregated results, got %#v", results)
	}
	for _, result := range results {
		if result.Status != "cancelled" || result.Text != "sub-agent interrupted" {
			t.Fatalf("expected cancelled aggregate result, got %#v", result)
		}
	}

	if len(emitted) != 6 {
		t.Fatalf("expected three starts and three cancel lifecycle deltas, got %#v", emitted)
	}
	cancelCount := 0
	for _, delta := range emitted {
		lifecycle, ok := delta.(contracts.DeltaTaskLifecycle)
		if !ok {
			continue
		}
		if lifecycle.Kind == "cancel" {
			cancelCount++
		}
	}
	if cancelCount != 3 {
		t.Fatalf("expected three cancel lifecycle deltas, got %#v", emitted)
	}
}
