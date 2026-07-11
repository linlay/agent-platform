package llm

import (
	"strings"
	"testing"

	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/contracts"
	"agent-platform/internal/frontendtools"
)

func TestPrepareToolCallTeamDelegateEmitsHiddenDispatch(t *testing.T) {
	members := []contracts.TeamMember{{Key: "writer", Name: "Writer"}, {Key: "reviewer", Name: "Reviewer"}}
	stream := &llmRunStream{
		engine: &LLMAgentEngine{tools: stubToolExecutor{}, frontend: frontendtools.NewDefaultRegistry()},
		session: contracts.QuerySession{
			RunID:       "run-team",
			TeamRuntime: &contracts.TeamRuntimeContext{MaxParallel: 2, Members: members},
		},
		execCtx: &contracts.ExecutionContext{},
	}

	invocation, deltas, toolMessage := stream.prepareToolCall(openAIToolCall{
		ID: "team-call", Type: "function",
		Function: openAIFunctionCall{Name: agentteam.ToolDelegate, Arguments: `{"mode":"fanout"}`},
	})
	if toolMessage != nil || len(deltas) != 0 || invocation == nil || !invocation.awaitExternalResult {
		t.Fatalf("unexpected preparation invocation=%#v deltas=%#v message=%#v", invocation, deltas, toolMessage)
	}
	if len(invocation.prelude) != 1 {
		t.Fatalf("prelude=%#v", invocation.prelude)
	}
	dispatch, ok := invocation.prelude[0].(contracts.DeltaTeamDispatch)
	if !ok || dispatch.Kind != agentteam.DispatchKindFanout || dispatch.DelegateMode != agentteam.DelegateModeFanout || len(dispatch.Tasks) != 2 {
		t.Fatalf("unexpected Team dispatch %#v", invocation.prelude[0])
	}
	if dispatch.Tasks[0].SubAgentKey != "writer" || dispatch.Tasks[1].SubAgentKey != "reviewer" {
		t.Fatalf("fanout did not preserve roster order: %#v", dispatch.Tasks)
	}
}

func TestPrepareToolCallTeamInvokeValidatesFrozenRoster(t *testing.T) {
	stream := &llmRunStream{
		engine: &LLMAgentEngine{tools: stubToolExecutor{}, frontend: frontendtools.NewDefaultRegistry()},
		session: contracts.QuerySession{
			RunID:       "run-team",
			TeamRuntime: &contracts.TeamRuntimeContext{MaxParallel: 1, Members: []contracts.TeamMember{{Key: "writer"}}},
		},
		execCtx: &contracts.ExecutionContext{},
	}

	invocation, deltas, toolMessage := stream.prepareToolCall(openAIToolCall{
		ID: "team-call", Type: "function",
		Function: openAIFunctionCall{Name: agentteam.ToolInvoke, Arguments: `{"tasks":[{"memberKey":"outside","task":"do work"}]}`},
	})
	if invocation != nil || len(deltas) != 1 || toolMessage == nil {
		t.Fatalf("expected immediate roster validation error, invocation=%#v deltas=%#v message=%#v", invocation, deltas, toolMessage)
	}
	result, ok := deltas[0].(contracts.DeltaToolResult)
	if !ok || result.Result.Error != "invalid_tool_arguments" {
		t.Fatalf("unexpected Team validation result %#v", deltas[0])
	}
}

func TestMergeToolDefinitionsKeepsTeamToolsSessionLocal(t *testing.T) {
	local := agentteam.HiddenToolDefinitions([]agentteam.MemberSpec{{Key: "writer"}}, 1)
	merged := mergeToolDefinitions(nil, local)
	if len(merged) != 2 || merged[0].Name != agentteam.ToolDelegate || merged[1].Name != agentteam.ToolInvoke {
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

func TestTeamRequiredRouteSuppressesTextAndCorrectsOnlyOnce(t *testing.T) {
	stream := &llmRunStream{
		engine:     &LLMAgentEngine{},
		session:    contracts.QuerySession{RunID: "run-team", TeamRuntime: &contracts.TeamRuntimeContext{}},
		execCtx:    &contracts.ExecutionContext{},
		toolChoice: "required",
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
	if stream.modelTerminalError == nil || !strings.Contains(stream.modelTerminalError.Error(), "did not produce a valid routing tool call") {
		t.Fatalf("second invalid route was not terminated: %v", stream.modelTerminalError)
	}
}
