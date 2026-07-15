package llm

import (
	"context"
	"strings"
	"testing"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
)

func TestPrepareToolCallTeamDelegateEmitsHiddenDispatch(t *testing.T) {
	members := []contracts.TeamMember{{Key: "writer", Name: "Writer"}, {Key: "reviewer", Name: "Reviewer"}}
	stream := &llmRunStream{
		engine: &LLMAgentEngine{tools: stubToolExecutor{}, frontend: frontendtools.NewDefaultRegistry()},
		session: contracts.QuerySession{
			RunID:       "run-team",
			Mode:        agentteam.Mode,
			TeamRuntime: &contracts.TeamRuntimeContext{MaxParallel: 2, Members: members},
		},
		execCtx: &contracts.ExecutionContext{},
	}

	invocation, deltas, toolMessage := stream.prepareToolCall(openAIToolCall{
		ID: "team-call", Type: "function",
		Function: openAIFunctionCall{Name: agentteam.ToolDelegate, Arguments: `{"tasks":[{"agentKey":"writer"},{"agentKey":"reviewer","task":"review"}]}`},
	})
	if toolMessage != nil || len(deltas) != 0 || invocation == nil || !invocation.awaitExternalResult {
		t.Fatalf("unexpected preparation invocation=%#v deltas=%#v message=%#v", invocation, deltas, toolMessage)
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
		engine: &LLMAgentEngine{tools: stubToolExecutor{}, frontend: frontendtools.NewDefaultRegistry()},
		session: contracts.QuerySession{
			RunID:       "run-team",
			Mode:        agentteam.Mode,
			TeamRuntime: &contracts.TeamRuntimeContext{MaxParallel: 1, Members: []contracts.TeamMember{{Key: "writer"}}},
		},
		execCtx: &contracts.ExecutionContext{},
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
}

func TestPrepareToolCallRejectsAgentDelegateOutsideTeam(t *testing.T) {
	stream := &llmRunStream{
		engine:  &LLMAgentEngine{tools: stubToolExecutor{}, frontend: frontendtools.NewDefaultRegistry()},
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
		engine:               &LLMAgentEngine{},
		session:              contracts.QuerySession{RunID: "run-team", TeamRuntime: &contracts.TeamRuntimeContext{}},
		execCtx:              &contracts.ExecutionContext{},
		toolChoice:           "auto",
		teamDelegateRequired: true,
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
	if stream.teamRouteCorrections != 1 || stream.modelTerminalError != nil || len(stream.messages) != 1 {
		t.Fatalf("first invalid route did not schedule exactly one correction: corrections=%d terminal=%v messages=%#v", stream.teamRouteCorrections, stream.modelTerminalError, stream.messages)
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
		frontendtools.NewDefaultRegistry(),
		contracts.NewNoopSandboxClient(),
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
	if !stream.teamDelegateRequired || !stream.teamRouteRequired() {
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

	stream.AllowOptionalTools()
	if stream.teamDelegateRequired || stream.teamRouteRequired() {
		t.Fatalf("Team delegation requirement should clear after a member dispatch: %#v", stream)
	}
}
