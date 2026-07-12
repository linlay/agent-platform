package coder

import (
	"encoding/json"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestPlanningContinuationDecisionAndSubmitDecision(t *testing.T) {
	answer := map[string]any{"planning": map[string]any{"decision": " Approve "}}
	if got := PlanningContinuationDecision(" PLANNING ", answer); got != "approve" {
		t.Fatalf("PlanningContinuationDecision=%q", got)
	}
	if got := PlanningContinuationDecision("question", answer); got != "" {
		t.Fatalf("non-planning decision=%q", got)
	}
	raw := json.RawMessage(`{"decision":"reject"}`)
	if got := SubmitPlanningDecision(api.SubmitParams{raw}); got != "reject" {
		t.Fatalf("SubmitPlanningDecision=%q", got)
	}
	if got := SubmitPlanningDecision(api.SubmitParams{raw, raw}); got != "" {
		t.Fatalf("multiple submit decisions must be rejected, got %q", got)
	}
	if !StartsNewExecutionRun("planning", answer, Mode, "") {
		t.Fatal("approved native CODER planning should start a new execution run")
	}
	if StartsNewExecutionRun("planning", answer, Mode, "codex") || StartsNewExecutionRun("planning", map[string]any{}, Mode, "") {
		t.Fatal("ACP CODER or non-approved planning must not start a native execution run")
	}
}

func TestBuildContinuationRequestPreservesOriginalAndAppliesIdentityPrecedence(t *testing.T) {
	stream := true
	input := ContinuationRequestInput{
		Original: api.QueryRequest{
			ChatID:       "original-chat",
			RunID:        "original-run",
			AgentKey:     "original-agent",
			TeamID:       "original-team",
			Message:      "original message",
			Params:       map[string]any{"keep": true},
			Stream:       &stream,
			IncludeUsage: true,
		},
		Submit: api.SubmitRequest{
			ChatID:            "submit-chat",
			RunID:             "source-run",
			AgentKey:          "submit-agent",
			AwaitingID:        "await-1",
			SubmitID:          "submit-1",
			ContinuationRunID: "continuation-run",
		},
		SummaryChatID:      "summary-chat",
		SummaryTeamID:      "summary-team",
		SummaryAgentKey:    "summary-agent",
		DefinitionAgentKey: "definition-agent",
		Mode:               "planning",
		Answer:             map[string]any{"planning": map[string]any{"decision": "approve"}},
		PlanningMarkdown:   "# Planning",
	}
	req := BuildContinuationRequest(input)
	if req.ChatID != "original-chat" || req.RunID != "continuation-run" || req.RequestID != "submit-1" || req.AgentKey != "submit-agent" || req.TeamID != "original-team" {
		t.Fatalf("unexpected resolved identity: %#v", req)
	}
	if req.Role != api.QueryRoleSystem || req.PlanningMode == nil || *req.PlanningMode || req.AccessLevel != contracts.AccessLevelDefault {
		t.Fatalf("unexpected continuation controls: %#v", req)
	}
	if req.Stream != &stream || !req.IncludeUsage || req.Params["keep"] != true {
		t.Fatalf("original request fields were not preserved: %#v", req)
	}
	if !strings.Contains(req.Message, "用户已经批准计划") || !strings.Contains(req.Message, "# Planning") || !strings.Contains(req.Message, "await-1") {
		t.Fatalf("unexpected continuation prompt %q", req.Message)
	}
}

func TestBuildPlanningApproveContinuationRequestMarksInternalParamsWithoutMutatingOriginal(t *testing.T) {
	originalParams := map[string]any{"keep": "value"}
	input := ContinuationRequestInput{
		Original:         api.QueryRequest{Message: "build it", Params: originalParams},
		Submit:           api.SubmitRequest{RunID: "source", ContinuationRunID: "execute", AwaitingID: "await"},
		SummaryChatID:    "chat",
		PlanningMarkdown: "# Confirmed",
	}
	req := BuildPlanningApproveContinuationRequest(input)
	if req.RunID != "execute" || req.Role != api.QueryRoleSystem || req.PlanningMode == nil || *req.PlanningMode {
		t.Fatalf("unexpected plan approve request: %#v", req)
	}
	if !strings.Contains(req.Message, "Original request:\nbuild it") || !strings.Contains(req.Message, "Confirmed planning:\n# Confirmed") {
		t.Fatalf("unexpected execute prompt %q", req.Message)
	}
	if !IsPlanningApproveContinuationParams(req.Params) || originalParams[PlanningApproveContinuationParam] != nil {
		t.Fatalf("continuation marker must be added to a clone, req=%#v original=%#v", req.Params, originalParams)
	}
}

func TestContinuationPromptKeepsGenericAwaitingBehavior(t *testing.T) {
	got := ContinuationPrompt("question", "await-2", map[string]any{"value": "yes"}, "")
	if !strings.Contains(got, "不要重复提问同一个问题") || !strings.Contains(got, "await-2") || !strings.Contains(got, `"value": "yes"`) {
		t.Fatalf("unexpected generic continuation prompt %q", got)
	}
}
