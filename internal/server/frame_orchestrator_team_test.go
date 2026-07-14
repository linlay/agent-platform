package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentcontract "agent-platform/internal/agent"
	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

func newTeamFrameOrchestrator(t *testing.T, main *stubOrchestratableStream, children map[string]contracts.AgentStream, defs map[string]catalog.AgentDefinition, routed *[]stream.StreamInput, emitted *[]contracts.AgentDelta) *frameOrchestrator {
	t.Helper()
	teamDef := catalog.TeamDefinition{
		TeamID:      "research",
		Name:        "Research",
		RuntimeMode: catalog.TeamRuntimeModeOrchestrated,
		AgentKeys:   []string{"writer", "reviewer"},
		Orchestrator: catalog.TeamOrchestratorConfig{
			ModelKey: "mock-model", MaxParallel: 2,
		},
	}
	snapshot := catalog.NewTeamSnapshot(teamDef, defs)
	o := newTestFrameOrchestrator(&orchestratorAgentEngine{streamsByAgentKey: children}, defs, emitted, routed)
	o.request = api.QueryRequest{RequestID: "req-team", RunID: "run_1", ChatID: "chat_1", TeamID: "research", Role: api.QueryRoleUser, Message: "original request"}
	o.session = contracts.QuerySession{
		RequestID: "req-team", RunID: "run_1", ChatID: "chat_1", TeamID: "research", Mode: agentteam.Mode,
		ModeCapabilities: agentcontract.ModeCapabilities{InvokeChildren: true},
	}
	o.teamSnapshot = &snapshot
	o.buildQuerySession = func(_ context.Context, req api.QueryRequest, _ chat.Summary, def catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
		if !options.IncludeHistory || options.IncludeMemory || options.AllowInvokeAgents {
			t.Fatalf("unexpected Team member options: %#v", options)
		}
		if req.Message != "original request" {
			t.Fatalf("member received %q, want original request", req.Message)
		}
		return contracts.QuerySession{RunID: req.RunID, ChatID: req.ChatID, AgentKey: def.Key, Mode: def.Mode}, nil
	}
	_ = main
	return o
}

func TestFrameOrchestratorTeamSingleDelegationReturnsMemberResultToCoordinator(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool",
		Tasks:      []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}},
	}}}
	child := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "member answer"}}, finalText: "member answer"}
	defs := map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
	}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, map[string]contracts.AgentStream{"writer": child}, defs, &routed, &emitted)

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if len(main.injected) != 1 || main.injected[0].isError || !strings.Contains(main.injected[0].text, `"agentKey":"writer"`) || !strings.Contains(main.injected[0].text, `"content":"member answer"`) {
		t.Fatalf("single delegation did not return a structured result: %#v", main.injected)
	}
	if !main.optionalToolsAllowed {
		t.Fatal("single delegation did not return normal coordinator control")
	}
	if len(emitted) != 2 {
		t.Fatalf("single delegation lifecycle count=%d, want 2: %#v", len(emitted), emitted)
	}
	found := false
	for _, input := range routed {
		content, ok := input.(stream.ContentDelta)
		if !ok {
			continue
		}
		found = true
		if content.TaskID == "" || content.AgentKey != "writer" || content.TeamID != "research" || content.Presentation != "task" || content.ActorType != "agent" {
			t.Fatalf("unexpected delegated content metadata %#v", content)
		}
	}
	if !found {
		t.Fatalf("no delegated member content routed: %#v", routed)
	}
}

func TestFrameOrchestratorTeamMultiDelegationUsesSameCoordinatorReturnPath(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool",
		Tasks:      []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}, {SubAgentKey: "reviewer"}},
	}}}
	defs := map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
	}
	children := map[string]contracts.AgentStream{
		"writer":   &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "writer answer"}}, finalText: "writer answer"},
		"reviewer": &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaContent{Text: "reviewer answer"}}, finalText: "reviewer answer"},
	}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, children, defs, &routed, &emitted)

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if len(main.injected) != 1 || main.injected[0].isError || !strings.Contains(main.injected[0].text, "writer answer") || !strings.Contains(main.injected[0].text, "reviewer answer") {
		t.Fatalf("unexpected multi-member result %#v", main.injected)
	}
	if !main.optionalToolsAllowed {
		t.Fatal("multi-member delegation did not use normal coordinator return path")
	}
	if len(emitted) != 4 {
		t.Fatalf("delegation lifecycle count=%d, want 4: %#v", len(emitted), emitted)
	}
	memberReplies := 0
	for _, input := range routed {
		content, ok := input.(stream.ContentDelta)
		if !ok {
			continue
		}
		memberReplies++
		if content.TaskID == "" || content.TeamID != "research" || content.Presentation != "task" || content.AgentKey == "" {
			t.Fatalf("unexpected delegated content metadata %#v", content)
		}
	}
	if memberReplies != 2 {
		t.Fatalf("member replies=%d, want 2: %#v", memberReplies, routed)
	}
}

func TestFrameOrchestratorTeamDelegationRejectsDuplicateMemberSet(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool",
		Tasks:      []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}, {SubAgentKey: "writer"}},
	}}}
	defs := map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
	}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, nil, defs, &routed, &emitted)

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if len(main.injected) != 1 || !main.injected[0].isError || !strings.Contains(main.injected[0].text, "may only appear once") {
		t.Fatalf("duplicate delegation was not rejected: %#v", main.injected)
	}
	if len(routed) != 0 || len(emitted) != 0 {
		t.Fatalf("duplicate delegation started work: routed=%#v emitted=%#v", routed, emitted)
	}
}

func TestFrameOrchestratorTeamCustomTaskUsesSameDelegationPath(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool",
		Tasks:      []contracts.SubAgentTaskSpec{{SubAgentKey: "writer", TaskText: "draft", TaskName: "Draft"}},
	}}}
	defs := map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT", VisibilityScopes: []string{"invoke"}},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT", VisibilityScopes: []string{"invoke"}},
	}
	child := &stubOrchestratableStream{finalText: "draft result"}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, map[string]contracts.AgentStream{"writer": child}, defs, &routed, &emitted)
	o.request.Role = api.QueryRoleAutomation
	o.request.Scene = &api.Scene{URL: "https://example.test", Title: "Example"}
	o.request.References = []api.Reference{{ID: "original-reference", Name: "source.md"}}
	o.buildQuerySession = func(_ context.Context, req api.QueryRequest, _ chat.Summary, def catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
		if req.Message != "draft" || !options.IncludeHistory || options.AllowInvokeAgents || options.TeamHistoryAgentKey != "writer" {
			t.Fatalf("unexpected delegation request/options: %#v %#v", req, options)
		}
		if req.Role != api.QueryRoleAutomation || req.Scene != o.request.Scene || len(req.References) != 1 || req.References[0].ID != "original-reference" {
			t.Fatalf("original role, scene, and references were not inherited: %#v", req)
		}
		return contracts.QuerySession{RunID: req.RunID, ChatID: req.ChatID, AgentKey: def.Key, Mode: def.Mode}, nil
	}

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if len(main.injected) != 1 || main.injected[0].isError || !main.optionalToolsAllowed {
		t.Fatalf("custom delegation did not return result and release required routing gate: injected=%#v optional=%v", main.injected, main.optionalToolsAllowed)
	}
}

func TestFrameOrchestratorTeamDelegationMergesFilesWithOriginalReferences(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool",
		Tasks:      []contracts.SubAgentTaskSpec{{SubAgentKey: "writer", Files: []string{"/workspace/draft.md", "/workspace/draft.md"}}},
	}}}
	defs := map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT"},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT"},
	}
	child := &stubOrchestratableStream{finalText: "done"}
	o := newTeamFrameOrchestrator(t, main, map[string]contracts.AgentStream{"writer": child}, defs, nil, nil)
	store, err := chat.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.ChatDir("chat_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.ChatDir("chat_1"), "draft.md"), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	o.chats = store
	o.request.References = []api.Reference{{ID: "original", Type: "file", Name: "source.md", Path: "/workspace/source.md"}}
	var childRequest api.QueryRequest
	o.buildQuerySession = func(_ context.Context, req api.QueryRequest, _ chat.Summary, def catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
		childRequest = req
		return contracts.QuerySession{RunID: req.RunID, ChatID: req.ChatID, AgentKey: def.Key, Mode: def.Mode}, nil
	}

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if len(childRequest.References) != 2 || childRequest.References[0].ID != "original" || childRequest.References[1].Path != "/workspace/draft.md" {
		t.Fatalf("files and original references were not merged and deduplicated: %#v", childRequest.References)
	}
}

func TestDeduplicateTeamReferencesMatchesAnyStableIdentity(t *testing.T) {
	got := deduplicateTeamReferences([]api.Reference{
		{ID: "original", Path: "/workspace/a.md"},
		{ID: "generated", Path: "/workspace/a.md"},
		{ID: "other", Path: "/workspace/b.md"},
	})
	if len(got) != 2 || got[0].ID != "original" || got[1].ID != "other" {
		t.Fatalf("deduplicated references=%#v", got)
	}
}

func TestRouteChildStreamInputAttributesModelAndUsageEventsToTask(t *testing.T) {
	const taskID = "team-task-1"
	for _, input := range []stream.StreamInput{
		stream.InputLLMRequest{ModelKey: "member-model"},
		stream.InputUsageSnapshot{ModelKey: "member-model", LLMReturnTotalTokens: 12},
		stream.InputRunActivity{Phase: "model", Status: "running"},
	} {
		routed := routeChildStreamInput("parent-run", taskID, input)
		switch value := routed.(type) {
		case stream.InputLLMRequest:
			if value.TaskID != taskID {
				t.Fatalf("llm request taskId=%q", value.TaskID)
			}
		case stream.InputUsageSnapshot:
			if value.TaskID != taskID {
				t.Fatalf("usage snapshot taskId=%q", value.TaskID)
			}
		case stream.InputRunActivity:
			if value.TaskID != taskID {
				t.Fatalf("run activity taskId=%q", value.TaskID)
			}
		default:
			t.Fatalf("unexpected routed input %T", routed)
		}
	}
}

func TestRouteTeamChildLLMRequestCarriesHiddenPersistenceActor(t *testing.T) {
	task := preparedSubTask{spec: contracts.SubAgentTaskSpec{SubAgentKey: "writer"}, taskID: "task-1"}
	input := routeChildStreamInput("run-1", task.taskID, stream.InputLLMRequest{ModelKey: "member-model"})
	routed, ok := routeTeamChildStreamInput("run-1", "research", task, input, childRunOptions{Presentation: "task"}).(stream.InputLLMRequest)
	if !ok || routed.TaskID != "task-1" || routed.ActorType != "agent" || routed.TeamID != "research" || routed.AgentKey != "writer" || routed.Presentation != "task" {
		t.Fatalf("routed Team llm.request=%#v", routed)
	}
}
