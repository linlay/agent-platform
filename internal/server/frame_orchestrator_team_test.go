package server

import (
	"context"
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

func TestFrameOrchestratorTeamDirectPublishesMemberAsRootAndTerminates(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool", Kind: agentteam.DispatchKindDirect, DelegateMode: agentteam.DelegateModeDirect,
		Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}},
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
	if len(main.injected) != 0 {
		t.Fatalf("direct success must not return to coordinator: %#v", main.injected)
	}
	if len(emitted) != 0 {
		t.Fatalf("direct should not create a task card: %#v", emitted)
	}
	found := false
	for _, input := range routed {
		content, ok := input.(stream.ContentDelta)
		if !ok {
			continue
		}
		found = true
		if content.TaskID != "" || content.AgentKey != "writer" || content.TeamID != "research" || content.Presentation != "reply" || content.ActorType != "agent" {
			t.Fatalf("unexpected direct content metadata %#v", content)
		}
	}
	if !found {
		t.Fatalf("no direct member content routed: %#v", routed)
	}
}

func TestFrameOrchestratorTeamFanoutShowsAllRepliesThenRequiresSummary(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool", Kind: agentteam.DispatchKindFanout, DelegateMode: agentteam.DelegateModeFanout,
		Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}, {SubAgentKey: "reviewer"}},
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
		t.Fatalf("unexpected fanout result %#v", main.injected)
	}
	if !main.finalResponseRequired {
		t.Fatal("fanout did not force a tool-free summary turn")
	}
	if len(emitted) != 4 {
		t.Fatalf("fanout lifecycle count=%d, want 4: %#v", len(emitted), emitted)
	}
	memberReplies := 0
	for _, input := range routed {
		content, ok := input.(stream.ContentDelta)
		if !ok {
			continue
		}
		memberReplies++
		if content.TaskID == "" || content.TeamID != "research" || content.Presentation != "reply" || content.AgentKey == "" {
			t.Fatalf("unexpected fanout content metadata %#v", content)
		}
	}
	if memberReplies != 2 {
		t.Fatalf("member replies=%d, want 2: %#v", memberReplies, routed)
	}
}

func TestFrameOrchestratorTeamFanoutRejectsDuplicateMemberSet(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool", Kind: agentteam.DispatchKindFanout, DelegateMode: agentteam.DelegateModeFanout,
		Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer"}, {SubAgentKey: "writer"}},
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
	if len(main.injected) != 1 || !main.injected[0].isError || !strings.Contains(main.injected[0].text, "exactly once") {
		t.Fatalf("duplicate fanout was not rejected: %#v", main.injected)
	}
	if len(routed) != 0 || len(emitted) != 0 {
		t.Fatalf("duplicate fanout started work: routed=%#v emitted=%#v", routed, emitted)
	}
}

func TestFrameOrchestratorTeamInvokeAllowsCoordinatorToFinishOrDispatchNextBatch(t *testing.T) {
	main := &stubOrchestratableStream{deltas: []contracts.AgentDelta{contracts.DeltaTeamDispatch{
		MainToolID: "team-tool", Kind: agentteam.DispatchKindInvoke,
		Tasks: []contracts.SubAgentTaskSpec{{SubAgentKey: "writer", TaskText: "draft", TaskName: "Draft"}},
	}}}
	defs := map[string]catalog.AgentDefinition{
		"writer":   {Key: "writer", Name: "Writer", Mode: "REACT", VisibilityScopes: []string{"invoke"}},
		"reviewer": {Key: "reviewer", Name: "Reviewer", Mode: "REACT", VisibilityScopes: []string{"invoke"}},
	}
	child := &stubOrchestratableStream{finalText: "draft result"}
	var routed []stream.StreamInput
	var emitted []contracts.AgentDelta
	o := newTeamFrameOrchestrator(t, main, map[string]contracts.AgentStream{"writer": child}, defs, &routed, &emitted)
	// team_invoke tasks are authored by the coordinator, not the original user.
	o.buildQuerySession = func(_ context.Context, req api.QueryRequest, _ chat.Summary, def catalog.AgentDefinition, options querySessionBuildOptions) (contracts.QuerySession, error) {
		if req.Message != "draft" || options.IncludeHistory || options.AllowInvokeAgents {
			t.Fatalf("unexpected invoke request/options: %#v %#v", req, options)
		}
		return contracts.QuerySession{RunID: req.RunID, ChatID: req.ChatID, AgentKey: def.Key, Mode: def.Mode}, nil
	}

	failed, interrupted, err := o.Run(main)
	if err != nil || failed || interrupted {
		t.Fatalf("Run() = failed=%v interrupted=%v err=%v", failed, interrupted, err)
	}
	if len(main.injected) != 1 || main.injected[0].isError || !main.optionalToolsAllowed {
		t.Fatalf("invoke did not return result and release required routing gate: injected=%#v optional=%v", main.injected, main.optionalToolsAllowed)
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
	routed, ok := routeTeamChildStreamInput("run-1", "research", task, input, childRunOptions{Presentation: "reply"}).(stream.InputLLMRequest)
	if !ok || routed.TaskID != "task-1" || routed.ActorType != "agent" || routed.TeamID != "research" || routed.AgentKey != "writer" || routed.Presentation != "reply" {
		t.Fatalf("routed Team llm.request=%#v", routed)
	}
}
