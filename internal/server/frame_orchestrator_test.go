package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
	"agent-platform/internal/llm"
	"agent-platform/internal/stream"
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

var _ contracts.OrchestratableAgentStream = (*stubOrchestratableStream)(nil)
var _ contracts.OrchestratableAgentStream = (*blockingOrchestratableStream)(nil)

func readServerTestJSONLines(store *chat.FileStore, chatID string) ([]map[string]any, error) {
	path := filepath.Join(filepath.Dir(store.ChatDir(chatID)), chatID+".jsonl")
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []map[string]any
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func textFromServerTestContent(value any) string {
	parts, _ := value.([]any)
	var text string
	for _, rawPart := range parts {
		part, _ := rawPart.(map[string]any)
		if partText, _ := part["text"].(string); partText != "" {
			text += partText
		}
	}
	return text
}

func newInvokeAgentsDelta(tasks ...contracts.SubAgentTaskSpec) contracts.DeltaInvokeSubAgents {
	return contracts.DeltaInvokeSubAgents{
		MainToolID: "tool_main_1",
		Tasks:      tasks,
	}
}

func newTestFrameOrchestrator(agent contracts.AgentEngine, registry map[string]catalog.AgentDefinition, emitted *[]contracts.AgentDelta, routed *[]stream.StreamInput) *frameOrchestrator {
	return newTestFrameOrchestratorWithContext(context.Background(), agent, registry, emitted, routed)
}

func testInvocableAgentRegistry(registry map[string]catalog.AgentDefinition) map[string]catalog.AgentDefinition {
	out := make(map[string]catalog.AgentDefinition, len(registry))
	for key, def := range registry {
		if len(def.VisibilityScopes) == 0 {
			def.VisibilityScopes = []string{"invoke"}
		}
		out[key] = def
	}
	return out
}

func newTestFrameOrchestratorWithContext(runCtx context.Context, agent contracts.AgentEngine, registry map[string]catalog.AgentDefinition, emitted *[]contracts.AgentDelta, routed *[]stream.StreamInput) *frameOrchestrator {
	return &frameOrchestrator{
		runCtx:  runCtx,
		request: api.QueryRequest{RunID: "run_1", ChatID: "chat_1", TeamID: "team_1"},
		session: contracts.QuerySession{RunID: "run_1", ChatID: "chat_1", Mode: "REACT"},
		summary: chat.Summary{ChatID: "chat_1", ChatName: "demo"},
		agent:   agent,
		registry: orchestratorRegistry{
			agents: testInvocableAgentRegistry(registry),
		},
		buildQuerySession: func(_ context.Context, req api.QueryRequest, _ chat.Summary, agentDef catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
			if options.IncludeHistory || options.IncludeMemory || options.AllowInvokeAgents {
				t := "unexpected sub-agent session build options"
				panic(t)
			}
			return contracts.QuerySession{
				RunID:         req.RunID,
				ChatID:        req.ChatID,
				AgentKey:      agentDef.Key,
				Mode:          agentDef.Mode,
				WorkspaceRoot: agentDef.Workspace.Root,
			}, nil
		},
		mapper: llm.NewDeltaMapper("run_1", "chat_1", contracts.Budget{Hitl: contracts.HitlPolicy{Timeout: 5}}, nil, frontendtools.NewDefaultRegistry()),
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
	if len(mainStream.injected) != 1 || !mainStream.injected[0].isError || mainStream.injected[0].text != "sub-agent must be REACT/ONESHOT/CODER/PROXY" {
		t.Fatalf("expected error tool result injected into main stream, got %#v", mainStream.injected)
	}
}

func TestFrameOrchestratorRejectsSubAgentWithoutInvokeScope(t *testing.T) {
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(contracts.SubAgentTaskSpec{
				SubAgentKey: "nav-only",
				TaskText:    "run hidden task",
			}),
		},
	}
	var emitted []contracts.AgentDelta
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{}, map[string]catalog.AgentDefinition{
		"nav-only": {Key: "nav-only", Mode: "REACT", VisibilityScopes: []string{"nav"}},
	}, &emitted, nil)

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}
	if len(emitted) != 0 {
		t.Fatalf("expected no task lifecycle deltas on scope reject, got %#v", emitted)
	}
	if len(mainStream.injected) != 1 || !mainStream.injected[0].isError || mainStream.injected[0].text != "sub-agent is not invocable" {
		t.Fatalf("expected scope error tool result, got %#v", mainStream.injected)
	}
}

func TestFrameOrchestratorAllowsInternalSubAgent(t *testing.T) {
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(contracts.SubAgentTaskSpec{
				SubAgentKey: "internal-worker",
				TaskText:    "run internal task",
			}),
		},
	}
	var emitted []contracts.AgentDelta
	child := &stubOrchestratableStream{
		deltas:    []contracts.AgentDelta{contracts.DeltaContent{Text: "internal output"}},
		finalText: "internal done",
	}
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{streams: []contracts.AgentStream{child}}, map[string]catalog.AgentDefinition{
		"internal-worker": {Key: "internal-worker", Mode: "REACT", VisibilityScopes: []string{"internal"}},
	}, &emitted, nil)

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}
	if len(mainStream.injected) != 1 || mainStream.injected[0].isError {
		t.Fatalf("expected internal sub-agent result, got %#v", mainStream.injected)
	}
}

func TestFrameOrchestratorRunsProxySubAgent(t *testing.T) {
	var upstreamPayload map[string]any
	workspace := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/query" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("unexpected accept header %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("decode upstream payload: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"content.delta","contentId":"remote_c_1","delta":"remote "}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"content.delta","contentId":"remote_c_1","delta":"answer"}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"run.complete","runId":"remote_run"}`+"\n\n")
	}))
	defer upstream.Close()

	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(contracts.SubAgentTaskSpec{
				SubAgentKey: "remote-query",
				TaskText:    "查一下研发部",
				TaskName:    "云端查询",
			}),
		},
	}
	var emitted []contracts.AgentDelta
	var routed []stream.StreamInput
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{}, map[string]catalog.AgentDefinition{
		"remote-query": {
			Key:  "remote-query",
			Name: "Remote Query",
			Mode: "PROXY",
			Workspace: catalog.AgentWorkspaceConfig{
				Root: workspace,
			},
			ProxyConfig: &catalog.ProxyConfig{
				BaseURL:  upstream.URL,
				AgentKey: "16",
				ChatID:   "dc17be36-92b2-44a6-8076-6b724068a181",
			},
		},
	}, &emitted, &routed)

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}
	if upstreamPayload["agentKey"] != "16" || upstreamPayload["message"] != "查一下研发部" {
		t.Fatalf("unexpected upstream payload %#v", upstreamPayload)
	}
	if upstreamPayload["chatId"] != "dc17be36-92b2-44a6-8076-6b724068a181" {
		t.Fatalf("expected upstream proxy chatId override, got %#v", upstreamPayload)
	}
	params, _ := upstreamPayload["params"].(map[string]any)
	if params["cwd"] != workspace {
		t.Fatalf("expected proxy child cwd from workspace root, got %#v", upstreamPayload)
	}
	if _, ok := upstreamPayload["stream"]; ok {
		t.Fatalf("did not expect proxy child payload to force stream flag: %#v", upstreamPayload)
	}
	if _, ok := upstreamPayload["runId"]; ok {
		t.Fatalf("did not expect proxy child payload to reuse local runId: %#v", upstreamPayload)
	}
	if len(mainStream.injected) != 1 || mainStream.injected[0].isError {
		t.Fatalf("expected successful proxy aggregate, got %#v", mainStream.injected)
	}
	if !strings.Contains(mainStream.injected[0].text, "remote answer") {
		t.Fatalf("expected proxy final text in aggregate, got %s", mainStream.injected[0].text)
	}
	if len(routed) != 2 {
		t.Fatalf("expected two routed content deltas, got %#v", routed)
	}
	for _, input := range routed {
		content, ok := input.(stream.ContentDelta)
		if !ok || content.TaskID == "" || !strings.Contains(content.ContentID, "remote_c_1") {
			t.Fatalf("expected routed proxy content delta, got %#v", input)
		}
	}
	if len(emitted) != 2 {
		t.Fatalf("expected start and complete lifecycle events, got %#v", emitted)
	}
}

func TestFrameOrchestratorMaterializesProxySubAgentFiles(t *testing.T) {
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("decode upstream payload: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"content.delta","payload":{"contentId":"remote_c_1","delta":"read file"}}`+"\n\n")
		_, _ = io.WriteString(w, `data: {"type":"run.complete","payload":{}}`+"\n\n")
	}))
	defer upstream.Close()

	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new chat store: %v", err)
	}
	chatDir := store.ChatDir("chat_1")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("create chat dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chatDir, "draft.md"), []byte("hello proxy"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(contracts.SubAgentTaskSpec{
				SubAgentKey: "remote",
				TaskText:    "read the draft",
				TaskName:    "远端阅读",
				Files:       []string{"/workspace/draft.md"},
			}),
		},
	}
	var emitted []contracts.AgentDelta
	var routed []stream.StreamInput
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{}, map[string]catalog.AgentDefinition{
		"remote": {
			Key:  "remote",
			Name: "Remote",
			Mode: "PROXY",
			ProxyConfig: &catalog.ProxyConfig{
				BaseURL: upstream.URL,
			},
		},
	}, &emitted, &routed)
	orchestrator.chats = store
	orchestrator.resourceBaseURL = "https://platform.example"
	orchestrator.resourceTickets = NewResourceTicketService(config.ResourceTicketConfig{Secret: "ticket-secret", TTLSeconds: 300})
	orchestrator.session.Subject = "student-1"

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}
	refs, ok := upstreamPayload["references"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("expected one upstream reference, got %#v", upstreamPayload["references"])
	}
	ref, ok := refs[0].(map[string]any)
	if !ok {
		t.Fatalf("expected reference map, got %#v", refs[0])
	}
	if ref["path"] != "/workspace/draft.md" || ref["name"] != "draft.md" {
		t.Fatalf("unexpected reference metadata %#v", ref)
	}
	refURL, _ := ref["url"].(string)
	if !strings.HasPrefix(refURL, "https://platform.example/api/resource?") || !strings.Contains(refURL, "t=") {
		t.Fatalf("expected absolute ticketed resource url, got %q", refURL)
	}
	parsed, err := url.Parse(refURL)
	if err != nil {
		t.Fatalf("parse ref url: %v", err)
	}
	if got := parsed.Query().Get("file"); got != "chat_1/draft.md" {
		t.Fatalf("unexpected file param %q", got)
	}
}

func TestFrameOrchestratorReportsProxySubAgentNestedError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"run.error","error":{"message":"remote permission denied"}}`+"\n\n")
	}))
	defer upstream.Close()

	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(contracts.SubAgentTaskSpec{
				SubAgentKey: "remote-query",
				TaskText:    "查一下研发部",
				TaskName:    "云端查询",
			}),
		},
	}
	var emitted []contracts.AgentDelta
	var routed []stream.StreamInput
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{}, map[string]catalog.AgentDefinition{
		"remote-query": {
			Key:  "remote-query",
			Name: "Remote Query",
			Mode: "PROXY",
			ProxyConfig: &catalog.ProxyConfig{
				BaseURL: upstream.URL,
			},
		},
	}, &emitted, &routed)

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}
	if len(mainStream.injected) != 1 || !mainStream.injected[0].isError {
		t.Fatalf("expected proxy error result, got %#v", mainStream.injected)
	}
	if !strings.Contains(mainStream.injected[0].text, "remote permission denied") {
		t.Fatalf("expected nested proxy error message, got %s", mainStream.injected[0].text)
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
	if !ok || startOne.Kind != "start" || startOne.TaskName != "写作" {
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

func TestFrameOrchestratorSubAgentRequestsShareRunIDWithUniqueRequestIDs(t *testing.T) {
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(
				contracts.SubAgentTaskSpec{SubAgentKey: "writer", TaskText: "write a summary", TaskName: "写作"},
				contracts.SubAgentTaskSpec{SubAgentKey: "reviewer", TaskText: "review it", TaskName: "审查"},
			),
		},
	}
	childOne := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			contracts.DeltaDebugLLMChat{ChatID: "chat_1", ModelKey: "mock", Status: "ok", RunSeq: 1},
			contracts.DeltaReasoning{Text: "thinking"},
			contracts.DeltaContent{Text: "child answer"},
		},
		finalText: "final child answer",
	}
	childTwo := &stubOrchestratableStream{finalText: "reviewed"}

	var mu sync.Mutex
	var subRequests []api.QueryRequest
	var subTaskIDs []string
	var emitted []contracts.AgentDelta
	var routed []stream.StreamInput
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{streamsByAgentKey: map[string]contracts.AgentStream{
		"writer":   childOne,
		"reviewer": childTwo,
	}}, map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
	}, &emitted, &routed)
	orchestrator.session.RequestID = "req_ABC"
	orchestrator.buildQuerySession = func(_ context.Context, req api.QueryRequest, _ chat.Summary, agentDef catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
		mu.Lock()
		subRequests = append(subRequests, req)
		subTaskIDs = append(subTaskIDs, options.SubTaskID)
		mu.Unlock()
		return contracts.QuerySession{
			RequestID: req.RequestID,
			RunID:     req.RunID,
			SubTaskID: options.SubTaskID,
			ChatID:    req.ChatID,
			AgentKey:  agentDef.Key,
			Mode:      agentDef.Mode,
		}, nil
	}

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}

	mu.Lock()
	requests := append([]api.QueryRequest(nil), subRequests...)
	gotSubTaskIDs := append([]string(nil), subTaskIDs...)
	mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected two sub-agent requests, got %#v", requests)
	}
	seenSubTaskIDs := map[string]bool{}
	for _, subTaskID := range gotSubTaskIDs {
		seenSubTaskIDs[subTaskID] = true
	}
	if len(gotSubTaskIDs) != 2 || !seenSubTaskIDs["sub_1"] || !seenSubTaskIDs["sub_2"] {
		t.Fatalf("expected sub-agent sandbox subTaskIDs [sub_1 sub_2], got %#v", gotSubTaskIDs)
	}
	wantTaskIDs := []string{"run_1_t_1", "run_1_t_2"}
	taskIDs := map[string]bool{}
	for index, delta := range emitted[:2] {
		lifecycle, ok := delta.(contracts.DeltaTaskLifecycle)
		if !ok || lifecycle.Kind != "start" || lifecycle.TaskID == "" {
			t.Fatalf("expected task.start lifecycle with task ID, got %#v", delta)
		}
		if lifecycle.TaskID != wantTaskIDs[index] {
			t.Fatalf("task ID = %q, want %q", lifecycle.TaskID, wantTaskIDs[index])
		}
		taskIDs[lifecycle.TaskID] = true
	}
	seenRequestIDs := map[string]bool{}
	for _, req := range requests {
		if req.RunID != "run_1" {
			t.Fatalf("expected sub-agent request to preserve parent run ID, got %#v", req)
		}
		if req.RequestID == "" {
			t.Fatalf("expected sub-agent request to have a unique request ID, got %#v", req)
		}
		if seenRequestIDs[req.RequestID] {
			t.Fatalf("expected unique sub-agent request IDs, got %#v", requests)
		}
		seenRequestIDs[req.RequestID] = true
	}
	for _, want := range []string{"req_ABC_sub_1", "req_ABC_sub_2"} {
		if !seenRequestIDs[want] {
			t.Fatalf("expected sub-agent request ID %q in %#v", want, requests)
		}
	}
	foundReasoning := false
	foundContent := false
	foundFinalContent := false
	foundLLMChat := false
	for _, input := range routed {
		switch value := input.(type) {
		case stream.InputDebugLLMChat:
			if value.TaskID == "run_1_t_1" {
				foundLLMChat = true
			}
		case stream.ReasoningDelta:
			if value.TaskID == "run_1_t_1" && value.ReasoningID == "run_1_t_1_r_1" {
				foundReasoning = true
			}
		case stream.ContentDelta:
			if value.TaskID == "run_1_t_1" && value.ContentID == "run_1_t_1_c_1" {
				foundContent = true
			}
			if value.TaskID == "run_1_t_1" && value.ContentID == "run_1_t_1:final" && value.Delta == "final child answer" {
				foundFinalContent = true
			}
		}
	}
	if !foundLLMChat || !foundReasoning || !foundContent || !foundFinalContent {
		t.Fatalf("expected child debug/reasoning/content/final IDs to use task prefix; llmChat=%v reasoning=%v content=%v final=%v routed=%#v", foundLLMChat, foundReasoning, foundContent, foundFinalContent, routed)
	}
}

func TestFrameOrchestratorSubAgentRequestIDsFallbackWhenParentMissing(t *testing.T) {
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(
				contracts.SubAgentTaskSpec{SubAgentKey: "writer", TaskText: "write a summary", TaskName: "写作"},
				contracts.SubAgentTaskSpec{SubAgentKey: "reviewer", TaskText: "review it", TaskName: "审查"},
			),
		},
	}

	var mu sync.Mutex
	var subRequests []api.QueryRequest
	var subTaskIDs []string
	var emitted []contracts.AgentDelta
	var routed []stream.StreamInput
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{streamsByAgentKey: map[string]contracts.AgentStream{
		"writer":   &stubOrchestratableStream{finalText: "written"},
		"reviewer": &stubOrchestratableStream{finalText: "reviewed"},
	}}, map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
	}, &emitted, &routed)
	orchestrator.buildQuerySession = func(_ context.Context, req api.QueryRequest, _ chat.Summary, agentDef catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
		mu.Lock()
		subRequests = append(subRequests, req)
		subTaskIDs = append(subTaskIDs, options.SubTaskID)
		mu.Unlock()
		return contracts.QuerySession{
			RequestID: req.RequestID,
			RunID:     req.RunID,
			SubTaskID: options.SubTaskID,
			ChatID:    req.ChatID,
			AgentKey:  agentDef.Key,
			Mode:      agentDef.Mode,
		}, nil
	}

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}

	mu.Lock()
	requests := append([]api.QueryRequest(nil), subRequests...)
	gotSubTaskIDs := append([]string(nil), subTaskIDs...)
	mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected two sub-agent requests, got %#v", requests)
	}
	seenSubTaskIDs := map[string]bool{}
	for _, subTaskID := range gotSubTaskIDs {
		seenSubTaskIDs[subTaskID] = true
	}
	if len(gotSubTaskIDs) != 2 || !seenSubTaskIDs["sub_1"] || !seenSubTaskIDs["sub_2"] {
		t.Fatalf("expected sub-agent sandbox subTaskIDs [sub_1 sub_2], got %#v", gotSubTaskIDs)
	}
	seenRequestIDs := map[string]bool{}
	for _, req := range requests {
		seenRequestIDs[req.RequestID] = true
	}
	for _, want := range []string{"sub_1", "sub_2"} {
		if !seenRequestIDs[want] {
			t.Fatalf("expected fallback sub-agent request ID %q in %#v", want, requests)
		}
	}
}

func TestFrameOrchestratorWritesSubAgentQueryAndSystemLines(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(
				contracts.SubAgentTaskSpec{SubAgentKey: "writer", TaskText: "write a summary", TaskName: "写作"},
				contracts.SubAgentTaskSpec{SubAgentKey: "writer", TaskText: "write another", TaskName: "再写"},
			),
		},
	}
	childOne := &stubOrchestratableStream{finalText: "first"}
	childTwo := &stubOrchestratableStream{finalText: "second"}
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{streams: []contracts.AgentStream{childOne, childTwo}}, map[string]catalog.AgentDefinition{
		"writer": {Key: "writer", Name: "Writer", Mode: "REACT"},
	}, nil, nil)
	orchestrator.chats = store
	orchestrator.prepareSystemInits = func(req api.QueryRequest, session *contracts.QuerySession, _ bool) ([]chat.QueryLineSystemInit, error) {
		return []chat.QueryLineSystemInit{{
			CacheKey:    "react:writer",
			Fingerprint: "sha256:writer",
			SystemMessage: map[string]any{
				"role":    "system",
				"content": "writer system",
			},
		}}, nil
	}
	orchestrator.buildChildSystems = func(api.QueryRequest, *contracts.QuerySession) []chat.QueryLineSystemInit {
		return []chat.QueryLineSystemInit{{
			CacheKey:    "react:writer",
			Fingerprint: "sha256:writer",
			SystemMessage: map[string]any{
				"role":    "system",
				"content": "writer system",
			},
		}}
	}

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}

	lines, err := readServerTestJSONLines(store, "chat_1")
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	var queryCount, embeddedSystemCount, standaloneSystemCount int
	for _, line := range lines {
		switch line["_type"] {
		case "query":
			if line["taskId"] == "" || line["subAgentKey"] != "writer" || line["taskToolId"] != "tool_main_1" {
				t.Fatalf("unexpected child query line %#v", line)
			}
			if _, ok := line["taskGroupId"]; ok {
				t.Fatalf("did not expect taskGroupId on child query line %#v", line)
			}
			systems, _ := line["systems"].([]any)
			if len(systems) == 0 {
				t.Fatalf("expected every child query line to carry systems, got %#v", line)
			}
			for _, rawSystem := range systems {
				system, _ := rawSystem.(map[string]any)
				for _, field := range []string{"mode", "stage", "agentKey"} {
					if _, ok := system[field]; ok {
						t.Fatalf("did not expect %s on child system init %#v", field, system)
					}
				}
			}
			embeddedSystemCount += len(systems)
			queryCount++
		case "system":
			standaloneSystemCount++
		}
	}
	if queryCount != 2 || embeddedSystemCount != 2 || standaloneSystemCount != 0 {
		t.Fatalf("expected two child query lines, two embedded systems, and no standalone system lines; queries=%d embedded=%d standalone=%d lines=%#v", queryCount, embeddedSystemCount, standaloneSystemCount, lines)
	}
}

func TestSubTaskReactStepPersistsContentMessage(t *testing.T) {
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("new file store: %v", err)
	}
	if _, _, err := store.EnsureChat("chat_1", "agent", "", "hello"); err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	mainStream := &stubOrchestratableStream{
		deltas: []contracts.AgentDelta{
			newInvokeAgentsDelta(contracts.SubAgentTaskSpec{SubAgentKey: "writer", TaskText: "write a summary", TaskName: "写作"}),
		},
	}
	child := &stubOrchestratableStream{finalText: "马到成功"}
	assembler := stream.NewAssembler(stream.StreamRequest{RunID: "run_1", ChatID: "chat_1"})
	mapper := llm.NewDeltaMapper("run_1", "chat_1", contracts.Budget{Hitl: contracts.HitlPolicy{Timeout: 5}}, nil, frontendtools.NewDefaultRegistry())
	writer := chat.NewStepWriter(store, "chat_1", "run_1", "react")
	orchestrator := newTestFrameOrchestrator(&orchestratorAgentEngine{streams: []contracts.AgentStream{child}}, map[string]catalog.AgentDefinition{
		"writer": {Key: "writer", Name: "Writer", Mode: "REACT"},
	}, nil, nil)
	orchestrator.chats = store
	orchestrator.mapper = mapper
	orchestrator.emitDelta = func(delta contracts.AgentDelta) {
		for _, input := range mapper.Map(delta) {
			for _, event := range assembler.Consume(input) {
				writer.OnEvent(event.Data())
			}
		}
	}
	orchestrator.emitInputs = func(inputs ...stream.StreamInput) {
		for _, input := range inputs {
			for _, event := range assembler.Consume(input) {
				writer.OnEvent(event.Data())
			}
		}
	}

	streamFailed, streamInterrupted, err := orchestrator.Run(mainStream)
	if err != nil || streamFailed || streamInterrupted {
		t.Fatalf("unexpected orchestrator result err=%v failed=%v interrupted=%v", err, streamFailed, streamInterrupted)
	}

	lines, err := readServerTestJSONLines(store, "chat_1")
	if err != nil {
		t.Fatalf("read chat jsonl: %v", err)
	}
	for _, line := range lines {
		if line["_type"] == "query" || line["taskId"] != "run_1_t_1" {
			continue
		}
		messages, _ := line["messages"].([]any)
		for _, rawMessage := range messages {
			message, _ := rawMessage.(map[string]any)
			content := textFromServerTestContent(message["content"])
			if message["role"] == "assistant" && content == "马到成功" {
				return
			}
		}
		t.Fatalf("expected sub task react step to include final content, got %#v", line)
	}
	t.Fatalf("expected sub task react step in jsonl, got %#v", lines)
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
		if !ok || lifecycle.Kind != "error" {
			continue
		}
		failedLifecycle = &lifecycle
		break
	}
	if failedLifecycle == nil || failedLifecycle.Error == nil {
		t.Fatalf("expected one error lifecycle delta in terminal events, got %#v", emitted)
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
