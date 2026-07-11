package coder

import (
	"encoding/json"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

type ContinuationRequestInput struct {
	Original           api.QueryRequest
	Submit             api.SubmitRequest
	SummaryChatID      string
	SummaryTeamID      string
	SummaryAgentKey    string
	DefinitionAgentKey string
	Mode               string
	Answer             map[string]any
	PlanMarkdown       string
}

func BuildContinuationRequest(input ContinuationRequestInput) api.QueryRequest {
	req := input.Original
	req.ChatID = firstNonBlank(req.ChatID, input.Submit.ChatID, input.SummaryChatID)
	req.RunID = firstNonBlank(input.Submit.ContinuationRunID, input.Submit.RunID, req.RunID)
	req.RequestID = firstNonBlank(input.Submit.SubmitID, req.RunID)
	req.AgentKey = firstNonBlank(input.Submit.AgentKey, req.AgentKey, input.SummaryAgentKey, input.DefinitionAgentKey)
	req.TeamID = firstNonBlank(req.TeamID, input.SummaryTeamID)
	req.Role = api.QueryRoleSystem
	if strings.EqualFold(input.Mode, "plan") {
		planningMode := false
		req.PlanningMode = &planningMode
	}
	req.Message = ContinuationPrompt(input.Mode, input.Submit.AwaitingID, input.Answer, input.PlanMarkdown)
	if strings.TrimSpace(req.AccessLevel) == "" {
		req.AccessLevel = contracts.AccessLevelDefault
	}
	return req
}

func BuildPlanApproveContinuationRequest(input ContinuationRequestInput) api.QueryRequest {
	req := input.Original
	originalMessage := strings.TrimSpace(req.Message)
	req.ChatID = firstNonBlank(req.ChatID, input.Submit.ChatID, input.SummaryChatID)
	req.RunID = firstNonBlank(input.Submit.ContinuationRunID, input.Submit.RunID, req.RunID)
	req.RequestID = firstNonBlank(input.Submit.SubmitID, req.RunID)
	req.AgentKey = firstNonBlank(input.Submit.AgentKey, req.AgentKey, input.SummaryAgentKey, input.DefinitionAgentKey)
	req.TeamID = firstNonBlank(req.TeamID, input.SummaryTeamID)
	req.Role = api.QueryRoleSystem
	planningMode := false
	req.PlanningMode = &planningMode
	req.Message = PlanApproveExecutePrompt(originalMessage, input.PlanMarkdown)
	req.Params = MarkPlanApproveContinuationParams(contracts.CloneMap(req.Params))
	if strings.TrimSpace(req.AccessLevel) == "" {
		req.AccessLevel = contracts.AccessLevelDefault
	}
	return req
}

func PlanContinuationDecision(mode string, answer map[string]any) string {
	if !strings.EqualFold(strings.TrimSpace(mode), "plan") {
		return ""
	}
	plan := contracts.AnyMapNode(answer["plan"])
	return strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(plan["decision"])))
}

func SubmitPlanDecision(params api.SubmitParams) string {
	items, err := api.DecodeSubmitParams(params)
	if err != nil || len(items) != 1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(items[0]["decision"])))
}

func StartsNewExecutionRun(mode string, answer map[string]any, agentMode string, acpBridgeID string) bool {
	return PlanContinuationDecision(mode, answer) == "approve" && IsNativeBackend(agentMode, acpBridgeID)
}

func ContinuationPrompt(mode string, awaitingID string, answer map[string]any, planMarkdown string) string {
	answerJSON, _ := json.MarshalIndent(answer, "", "  ")
	if !strings.EqualFold(strings.TrimSpace(mode), "plan") {
		return strings.TrimSpace("继续处理刚收到的等待项答案。不要重复提问同一个问题，直接根据答案继续完成原请求。\n\nAwaiting ID: " + awaitingID + "\n\nAnswer:\n" + string(answerJSON))
	}
	prefix := "继续处理刚收到的计划确认结果，不要再次请求同一个计划确认。"
	switch PlanContinuationDecision(mode, answer) {
	case "approve":
		prefix = "用户已经批准计划。请基于已确认计划继续执行，不要再次请求确认。"
	case "reject":
		prefix = "用户已经拒绝计划。请根据反馈修订方案或给出下一步，不要执行被拒绝的计划。"
	}
	if strings.TrimSpace(planMarkdown) != "" {
		return strings.TrimSpace(prefix + "\n\nAwaiting ID: " + awaitingID + "\n\nPlan:\n" + planMarkdown + "\n\nAnswer:\n" + string(answerJSON))
	}
	return strings.TrimSpace(prefix + "\n\nAwaiting ID: " + awaitingID + "\n\nAnswer:\n" + string(answerJSON))
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
