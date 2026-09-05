package llm

import (
	"context"
	"strings"
	"testing"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/testutil"
	"agent-platform/internal/toolinteraction"
)

func TestPrepareToolCallTeamDelegateEmitsHiddenDispatch(t *testing.T) {
	members := []contracts.TeamMember{{Key: "writer", Name: "Writer"}, {Key: "reviewer", Name: "Reviewer"}}
	stream := &llmRunStream{
		engine: &LLMAgentEngine{tools: stubToolExecutor{}, interactions: toolinteraction.NewDefaultRegistry()},
		session: contracts.QuerySession{
			RunID:       "run-team",
			Mode:        agentteam.Mode,
			TeamRuntime: &contracts.TeamRuntimeContext{MaxParallel: 2, Members: members},
		},
		execCtx:          &contracts.ExecutionContext{},
		teamStateMachine: agentteam.NewStateMachine(),
	}

	invocation, deltas, toolMessage := stream.prepareToolCall(openAIToolCall{
		ID: "team-call", Type: "function",
		Function: openAIFunctionCall{Name: agentteam.ToolDelegate, Arguments: `{"tasks":[{"agentKey":"writer"},{"agentKey":"reviewer","task":"review"}]}`},
	})
	if toolMessage != nil || len(deltas) != 0 || invocation == nil || !invocation.awaitExternalResult {
		t.Fatalf("unexpected preparation invocation=%#v deltas=%#v message=%#v", invocation, deltas, toolMessage)
	}
	if invocation.teamDispatch == nil || stream.teamStateMachine.Phase() != agentteam.PhaseRouting {
		t.Fatalf("Team dispatch changed state during parsing: invocation=%#v phase=%q", invocation, stream.teamStateMachine.Phase())
	}
	if len(invocation.prelude) != 1 {
		t.Fatalf("prelude=%#v", invocation.prelude)
	}
	dispatch, ok := invocation.prelude[0].(contracts.DeltaTeamDispatch)
	if !ok || len(dispatch.Tasks) != 2 {
		t.Fatalf("unexpected Team dispatch %#v", invocation.prelude[0])
	}
	if dispatch.Tasks[0].SubAgentKey != "writer" || dispatch.Tasks[1].SubAgentKey != "reviewer" {
		t.Fatalf("delegation did not preserve task order: %#v", dispatch.Tasks)
	}
	if dispatch.Tasks[0].TaskText != "" || dispatch.Tasks[1].TaskText != "review" {
		t.Fatalf("delegation task text=%#v", dispatch.Tasks)
	}
}

func TestPrepareToolCallTeamDelegateValidatesFrozenRoster(t *testing.T) {
	stream := &llmRunStream{
		engine: &LLMAgentEngine{tools: stubToolExecutor{}, interactions: toolinteraction.NewDefaultRegistry()},
		session: contracts.QuerySession{
			RunID:       "run-team",
			Mode:        agentteam.Mode,
			TeamRuntime: &contracts.TeamRuntimeContext{MaxParallel: 1, Members: []contracts.TeamMember{{Key: "writer"}}},
		},
		execCtx:          &contracts.ExecutionContext{},
		teamStateMachine: agentteam.NewStateMachine(),
	}

	invocation, deltas, toolMessage := stream.prepareToolCall(openAIToolCall{
		ID: "team-call", Type: "function",
		Function: openAIFunctionCall{Name: agentteam.ToolDelegate, Arguments: `{"tasks":[{"agentKey":"outside","task":"do work"}]}`},
	})
	if invocation != nil || len(deltas) != 1 || toolMessage == nil {
		t.Fatalf("expected immediate roster validation error, invocation=%#v deltas=%#v message=%#v", invocation, deltas, toolMessage)
	}
	result, ok := deltas[0].(contracts.DeltaToolResult)
	if !ok || result.Result.Error != "invalid_tool_arguments" {
		t.Fatalf("unexpected Team validation result %#v", deltas[0])
	}
	if stream.teamStateMachine.Phase() != agentteam.PhaseRouting {
		t.Fatalf("invalid Team dispatch changed phase to %q", stream.teamStateMachine.Phase())
	}
}

func TestTeamNonDelegateToolDoesNotSatisfyInitialRoute(t *testing.T) {
	stream := &llmRunStream{
		engine:           &LLMAgentEngine{tools: stubToolExecutor{}, interactions: toolinteraction.NewDefaultRegistry()},
		session:          contracts.QuerySession{RunID: "run-team", Mode: agentteam.Mode, TeamRuntime: &contracts.TeamRuntimeContext{}},
		execCtx:          &contracts.ExecutionContext{},
		teamStateMachine: agentteam.NewStateMachine(),
	}
	invocation, _, _ := stream.prepareToolCall(openAIToolCall{
		ID: "plan-call", Type: "function",
		Function: openAIFunctionCall{Name: contracts.PlanGetTasksToolName, Arguments: `{}`},
	})
	if invocation == nil {
		t.Fatal("planning tool was not prepared")
	}
	stream.queuedToolCalls = []*preparedToolInvocation{invocation}
	if err := stream.activateNextToolCall(); err != nil {
		t.Fatalf("activate planning tool: %v", err)
	}
	if !stream.teamRouteRequired() || stream.teamStateMachine.Phase() != agentteam.PhaseRouting {
		t.Fatalf("planning tool satisfied Team route: phase=%q", stream.teamStateMachine.Phase())
	}
}

func TestPrepareToolCallRejectsAgentDelegateOutsideTeam(t *testing.T) {
	stream := &llmRunStream{
		engine:  &LLMAgentEngine{tools: stubToolExecutor{}, interactions: toolinteraction.NewDefaultRegistry()},
		session: contracts.QuerySession{RunID: "run-agent", Mode: "REACT"},
		execCtx: &contracts.ExecutionContext{},
	}
	invocation, deltas, toolMessage := stream.prepareToolCall(openAIToolCall{
		ID: "delegate-call", Type: "function",
		Function: openAIFunctionCall{Name: agentteam.ToolDelegate, Arguments: `{"tasks":[{"agentKey":"writer"}]}`},
	})
	if invocation != nil || len(deltas) != 1 || toolMessage == nil {
		t.Fatalf("expected ordinary Agent rejection, invocation=%#v deltas=%#v message=%#v", invocation, deltas, toolMessage)
	}
	result, ok := deltas[0].(contracts.DeltaToolResult)
	if !ok || result.Result.Error != "internal_tool_only" {
		t.Fatalf("unexpected rejection result %#v", deltas)
	}
}

func TestMergeToolDefinitionsKeepsTeamToolSessionLocal(t *testing.T) {
	local := []api.ToolDetailResponse{{
		Key: agentteam.ToolDelegate, Name: agentteam.ToolDelegate,
		Meta: map[string]any{"clientVisible": false, "internalOnly": true},
	}}
	merged := mergeToolDefinitions(nil, local)
	if len(merged) != 1 || merged[0].Name != agentteam.ToolDelegate {
		t.Fatalf("merged definitions %#v", merged)
	}
	for _, definition := range merged {
		if visible, _ := definition.Meta["clientVisible"].(bool); visible {
			t.Fatalf("Team tool must be hidden: %#v", definition)
		}
		if internal, _ := definition.Meta["internalOnly"].(bool); !internal {
			t.Fatalf("Team tool must stay session-local: %#v", definition)
		}
	}
}

func TestTeamMandatoryRouteSuppressesTextAndCorrectsOnlyOnce(t *testing.T) {
	stream := &llmRunStream{
		engine:           &LLMAgentEngine{},
		session:          contracts.QuerySession{RunID: "run-team", TeamRuntime: &contracts.TeamRuntimeContext{}},
		execCtx:          &contracts.ExecutionContext{},
		toolChoice:       "auto",
		teamStateMachine: agentteam.NewStateMachine(),
	}

	stream.currentTurn = &providerTurnStream{finishReason: "stop"}
	stream.appendContentDelta("invalid ordinary answer")
	for _, delta := range stream.pending {
		if _, visible := delta.(contracts.DeltaContent); visible {
			t.Fatalf("invalid routing text became visible: %#v", stream.pending)
		}
	}
	stream.pending = nil
	if err := stream.finishCurrentTurn(); err != nil {
		t.Fatalf("first invalid route: %v", err)
	}
	if stream.teamStateMachine.Phase() != agentteam.PhaseRouting || stream.modelTerminalError != nil || len(stream.messages) != 1 {
		t.Fatalf("first invalid route did not schedule exactly one correction: phase=%q terminal=%v messages=%#v", stream.teamStateMachine.Phase(), stream.modelTerminalError, stream.messages)
	}
	if stream.messages[0].Role != "user" {
		t.Fatalf("correction message=%#v", stream.messages[0])
	}

	stream.pending = nil
	stream.currentTurn = &providerTurnStream{finishReason: "stop"}
	stream.appendContentDelta("still no route")
	stream.pending = nil
	if err := stream.finishCurrentTurn(); err != nil {
		t.Fatalf("second invalid route: %v", err)
	}
	if stream.modelTerminalError == nil || !strings.Contains(stream.modelTerminalError.Error(), "did not produce a valid agent_delegate call") {
		t.Fatalf("second invalid route was not terminated: %v", stream.modelTerminalError)
	}
	if stream.teamStateMachine.Phase() != agentteam.PhaseFailed {
		t.Fatalf("second invalid route phase=%q, want failed", stream.teamStateMachine.Phase())
	}
}

func TestTeamModeUsesAutoProviderToolChoiceAndRetainsMandatoryDelegation(t *testing.T) {
	tool := api.ToolDetailResponse{
		Name:        agentteam.ToolDelegate,
		Description: "delegate a Team task",
		Parameters:  map[string]any{"type": "object"},
	}
	engine := NewLLMAgentEngine(
		config.Config{},
		newSystemInitTestModelRegistry(t),
		stubToolExecutor{defs: []api.ToolDetailResponse{tool}},
		toolinteraction.NewDefaultRegistry(),
		testutil.NewNoopSandboxClient(),
	)
	session := contracts.QuerySession{
		RunID:        "run-team",
		ChatID:       "chat-team",
		AgentKey:     "__team__:research",
		AgentName:    "Research",
		Mode:         agentteam.Mode,
		ModelKey:     "mock-model",
		ToolNames:    []string{agentteam.ToolDelegate},
		TeamRuntime:  &contracts.TeamRuntimeContext{},
		PromptAppend: contracts.DefaultPromptAppendConfig(),
	}
	req := api.QueryRequest{RunID: session.RunID, ChatID: session.ChatID, Message: "research"}
	profiles, err := NewSystemInitProfileBuilder(engine.models, SystemInitDefaults{}).BuildSystemInitProfiles(contracts.SystemInitBuildInput{
		Session:         session,
		Request:         req,
		ToolDefinitions: []api.ToolDetailResponse{tool},
	})
	if err != nil {
		t.Fatalf("build Team system init profiles: %v", err)
	}
	session.SystemInitCache = make(map[string]contracts.SystemInitSnapshot, len(profiles))
	for _, profile := range profiles {
		session.SystemInitCache[profile.CacheKey] = contracts.SystemInitSnapshot{
			AgentKey:       profile.AgentKey,
			Fingerprint:    profile.Fingerprint,
			SystemMessage:  cloneAnyMapViaJSON(profile.SystemMessage),
			Tools:          cloneAnySlice(profile.Tools),
			Model:          cloneAnyMapViaJSON(profile.Model),
			ToolChoice:     profile.ToolChoice,
			RequestOptions: cloneAnyMapViaJSON(profile.RequestOptions),
		}
	}

	raw, err := (teamMode{}).Start(engine, context.Background(), req, session)
	if err != nil {
		t.Fatalf("start Team mode: %v", err)
	}
	stream, ok := raw.(*llmRunStream)
	if !ok {
		t.Fatalf("Team stream type = %T, want *llmRunStream", raw)
	}
	if stream.toolChoice != "auto" {
		t.Fatalf("Team provider toolChoice = %q, want auto", stream.toolChoice)
	}
	if stream.teamStateMachine == nil || !stream.teamRouteRequired() || stream.teamStateMachine.Phase() != agentteam.PhaseRouting {
		t.Fatalf("Team must retain its initial delegation requirement: %#v", stream)
	}

	prepared, err := stream.protocol.PrepareRequest(protocolStreamParams{
		runID:          session.RunID,
		provider:       stream.provider,
		model:          stream.model,
		protocolConfig: stream.protocolConfig,
		stageSettings:  stream.stageSettings,
		messages:       stream.messages,
		toolSpecs:      stream.toolSpecs,
		toolChoice:     stream.toolChoice,
	})
	if err != nil {
		t.Fatalf("prepare Team provider request: %v", err)
	}
	if got := prepared.RequestBody["tool_choice"]; got != "auto" {
		t.Fatalf("Team request tool_choice = %#v, want auto", got)
	}

	dispatch := agentteam.Dispatch{Tasks: []agentteam.TaskSpec{{AgentKey: "writer"}}}
	stream.queuedToolCalls = []*preparedToolInvocation{{
		toolID:              "team-call",
		toolName:            agentteam.ToolDelegate,
		awaitExternalResult: true,
		teamDispatch:        &dispatch,
	}}
	if err := stream.activateNextToolCall(); err != nil {
		t.Fatalf("activate Team dispatch: %v", err)
	}
	if stream.teamStateMachine.Phase() != agentteam.PhaseWaiting || stream.teamRouteRequired() {
		t.Fatalf("activated Team dispatch phase=%q required=%v", stream.teamStateMachine.Phase(), stream.teamRouteRequired())
	}
	if stream.InjectToolResult("wrong-call", `{"results":[]}`, false) {
		t.Fatal("wrong Team tool id unexpectedly injected a result")
	}
	if stream.teamStateMachine.Phase() != agentteam.PhaseWaiting {
		t.Fatalf("wrong Team tool id changed phase to %q", stream.teamStateMachine.Phase())
	}
	if !stream.InjectToolResult("team-call", `{"results":[]}`, false) {
		t.Fatal("active Team dispatch rejected its result")
	}
	if stream.teamRouteRequired() || stream.teamStateMachine.Phase() != agentteam.PhaseCoordinator {
		t.Fatalf("Team delegation result did not restore coordinator control: phase=%q", stream.teamStateMachine.Phase())
	}
	if err := stream.invokeActiveToolCall(); err != nil {
		t.Fatalf("consume first Team result: %v", err)
	}
	secondDispatch := agentteam.Dispatch{Tasks: []agentteam.TaskSpec{{AgentKey: "reviewer"}}}
	stream.queuedToolCalls = []*preparedToolInvocation{{
		toolID:              "team-call-2",
		toolName:            agentteam.ToolDelegate,
		awaitExternalResult: true,
		teamDispatch:        &secondDispatch,
	}}
	if err := stream.activateNextToolCall(); err != nil {
		t.Fatalf("activate second Team dispatch: %v", err)
	}
	if stream.teamStateMachine.Phase() != agentteam.PhaseWaiting {
		t.Fatalf("second Team dispatch phase=%q", stream.teamStateMachine.Phase())
	}
	if !stream.InjectToolResult("team-call-2", `{"results":[{"agentKey":"reviewer","status":"failed"}]}`, true) {
		t.Fatal("failed second Team dispatch rejected its result")
	}
	if stream.teamStateMachine.Phase() != agentteam.PhaseCoordinator {
		t.Fatalf("failed Team result did not return coordinator control: phase=%q", stream.teamStateMachine.Phase())
	}
}

func TestTeamCoordinatorPlainTextCompletesStateMachine(t *testing.T) {
	machine := agentteam.NewStateMachine()
	if err := machine.BeginDispatch(agentteam.Dispatch{Tasks: []agentteam.TaskSpec{{AgentKey: "writer"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := machine.FinishDispatch(); err != nil {
		t.Fatal(err)
	}
	stream := &llmRunStream{
		engine:           &LLMAgentEngine{},
		session:          contracts.QuerySession{RunID: "run-team", TeamRuntime: &contracts.TeamRuntimeContext{}},
		execCtx:          &contracts.ExecutionContext{},
		teamStateMachine: machine,
		currentTurn:      &providerTurnStream{finishReason: "stop"},
	}
	stream.appendContentDelta("coordinator final answer")
	if err := stream.finishCurrentTurn(); err != nil {
		t.Fatalf("finish coordinator answer: %v", err)
	}
	if machine.Phase() != agentteam.PhaseComplete || !stream.finished {
		t.Fatalf("coordinator answer phase=%q finished=%v", machine.Phase(), stream.finished)
	}
}

func TestTeamCoordinatorTailSteerKeepsStateMachineActive(t *testing.T) {
	machine := agentteam.NewStateMachine()
	if err := machine.BeginDispatch(agentteam.Dispatch{Tasks: []agentteam.TaskSpec{{AgentKey: "writer"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := machine.FinishDispatch(); err != nil {
		t.Fatal(err)
	}
	control := contracts.NewRunControl(context.Background(), "run-team")
	if !control.EnqueueSteer(api.SteerRequest{RunID: "run-team", Message: "continue with a review"}) {
		t.Fatal("enqueue Team steer")
	}
	stream := &llmRunStream{
		engine:           &LLMAgentEngine{},
		session:          contracts.QuerySession{RunID: "run-team", TeamRuntime: &contracts.TeamRuntimeContext{}},
		execCtx:          &contracts.ExecutionContext{},
		runControl:       control,
		teamStateMachine: machine,
		currentTurn:      &providerTurnStream{finishReason: "stop"},
	}
	stream.appendContentDelta("first coordinator answer")
	if err := stream.finishCurrentTurn(); err != nil {
		t.Fatalf("finish coordinator answer with steer: %v", err)
	}
	if machine.Phase() != agentteam.PhaseCoordinator || stream.finished {
		t.Fatalf("tail steer phase=%q finished=%v", machine.Phase(), stream.finished)
	}
}
