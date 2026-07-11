package coder

import (
	"encoding/json"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func TestPlanContinuationDecisionAndSubmitDecision(t *testing.T) {
	answer := map[string]any{"plan": map[string]any{"decision": " Approve "}}
	if got := PlanContinuationDecision(" PLAN ", answer); got != "approve" {
		t.Fatalf("PlanContinuationDecision=%q", got)
	}
	if got := PlanContinuationDecision("question", answer); got != "" {
		t.Fatalf("non-plan decision=%q", got)
	}
	raw := json.RawMessage(`{"decision":"reject"}`)
	if got := SubmitPlanDecision(api.SubmitParams{raw}); got != "reject" {
		t.Fatalf("SubmitPlanDecision=%q", got)
	}
	if got := SubmitPlanDecision(api.SubmitParams{raw, raw}); got != "" {
		t.Fatalf("multiple submit decisions must be rejected, got %q", got)
	}
	if !StartsNewExecutionRun("plan", answer, Mode, "") {
		t.Fatal("approved native CODER plan should start a new execution run")
	}
	if StartsNewExecutionRun("plan", answer, Mode, "codex") || StartsNewExecutionRun("plan", map[string]any{}, Mode, "") {
		t.Fatal("ACP CODER or non-approved plan must not start a native execution run")
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
		Mode:               "plan",
		Answer:             map[string]any{"plan": map[string]any{"decision": "approve"}},
		PlanMarkdown:       "# Plan",
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
	if !strings.Contains(req.Message, "用户已经批准计划") || !strings.Contains(req.Message, "# Plan") || !strings.Contains(req.Message, "await-1") {
		t.Fatalf("unexpected continuation prompt %q", req.Message)
	}
}

func TestBuildPlanApproveContinuationRequestMarksInternalParamsWithoutMutatingOriginal(t *testing.T) {
	originalParams := map[string]any{"keep": "value"}
	input := ContinuationRequestInput{
		Original:      api.QueryRequest{Message: "build it", Params: originalParams},
		Submit:        api.SubmitRequest{RunID: "source", ContinuationRunID: "execute", AwaitingID: "await"},
		SummaryChatID: "chat",
		PlanMarkdown:  "# Confirmed",
	}
	req := BuildPlanApproveContinuationRequest(input)
	if req.RunID != "execute" || req.Role != api.QueryRoleSystem || req.PlanningMode == nil || *req.PlanningMode {
		t.Fatalf("unexpected plan approve request: %#v", req)
	}
	if !strings.Contains(req.Message, "Original request:\nbuild it") || !strings.Contains(req.Message, "Confirmed plan:\n# Confirmed") {
		t.Fatalf("unexpected execute prompt %q", req.Message)
	}
	if !IsPlanApproveContinuationParams(req.Params) || originalParams[PlanApproveContinuationParam] != nil {
		t.Fatalf("continuation marker must be added to a clone, req=%#v original=%#v", req.Params, originalParams)
	}
}

func TestContinuationPromptKeepsGenericAwaitingBehavior(t *testing.T) {
	got := ContinuationPrompt("question", "await-2", map[string]any{"value": "yes"}, "")
	if !strings.Contains(got, "不要重复提问同一个问题") || !strings.Contains(got, "await-2") || !strings.Contains(got, `"value": "yes"`) {
		t.Fatalf("unexpected generic continuation prompt %q", got)
	}
}
