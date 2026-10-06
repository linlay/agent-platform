package platformcontrol

import (
	"agent-platform/internal/api"
	"agent-platform/internal/automation"
	"agent-platform/internal/contracts"
	"context"
	"encoding/json"
	"fmt"
)

func (h *ToolHandler) ConfigureAutomation(service *automation.Service) *ToolHandler {
	h.automations = service
	return h
}
func init() {
	candidate := map[string]string{"name": "s!", "description": "s", "cron": "s!", "agentKey": "s", "teamId": "s", "enabled": "b", "zoneId": "s", "remainingRuns": "n", "query": "o!"}
	argumentFields["automation_manage.create"] = candidate
	update := map[string]string{"id": "s!", "baseRevision": "s!"}
	for k, v := range candidate {
		if len(v) > 1 {
			v = v[:1]
		}
		update[k] = v
	}
	argumentFields["automation_manage.update"] = update
	argumentFields["automation_manage.setEnabled"] = map[string]string{"id": "s!", "baseRevision": "s!", "enabled": "b!"}
	for _, a := range []string{"delete", "trigger"} {
		argumentFields["automation_manage."+a] = map[string]string{"id": "s!", "baseRevision": "s!"}
	}
	argumentFields["automation_query.list"] = map[string]string{"limit": "n", "offset": "n"}
	argumentFields["automation_query.get"] = map[string]string{"id": "s!"}
	argumentFields["automation_query.executions"] = map[string]string{"id": "s!", "limit": "n", "offset": "n"}
	argumentFields["automation_query.execution"] = map[string]string{"executionId": "s!"}
	argumentFields["automation_query.validate"] = candidate
}
func automationInvocation(e *contracts.ExecutionContext) string {
	if e == nil || e.CurrentToolID == "" || e.Session.RunID == "" {
		return ""
	}
	b, _ := json.Marshal([]string{e.Session.Subject, e.Session.AgentKey, e.Session.RunID, e.CurrentToolID})
	return string(b)
}
func (h *ToolHandler) prepareAutomationApproval(_ context.Context, tool string, args map[string]any, e *contracts.ExecutionContext) (*contracts.ToolApproval, error) {
	action, p, err := h.admitted(tool, args, e)
	if err != nil {
		return nil, err
	}
	if h.automations == nil {
		return nil, fmt.Errorf("automation service unavailable")
	}
	key := automationInvocation(e)
	if key == "" {
		return nil, fmt.Errorf("automation invocation identity required")
	}
	plan, err := h.automations.PrepareControl(action, p, key)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	if h.registry != nil {
		for _, d := range []*api.AutomationDetailResponse{plan.Before, plan.After} {
			if d == nil {
				continue
			}
			if a, ok := h.registry.AgentDefinition(d.AgentKey); ok {
				names[d.AgentKey] = a.Name
			}
			if team, ok := h.registry.TeamDefinition(d.TeamID); ok {
				names[d.TeamID] = team.Name
			}
		}
	}
	return &contracts.ToolApproval{AllowAutoApprove: action != "delete", Fingerprint: contracts.ToolApprovalFingerprint(e, tool, action, plan.Digest), Title: "Automation / " + action, Form: map[string]any{"action": action, "before": plan.Before, "after": plan.After, "preview": plan.Preview, "baseRevision": plan.Revision, "names": names, "executionOptions": plan.ExecutionOptions}}, nil
}
func (h *ToolHandler) invokeAutomation(tool, action string, p map[string]any, e *contracts.ExecutionContext) (any, error) {
	if h.automations == nil {
		return nil, fmt.Errorf("automation service unavailable")
	}
	if tool == "automation_manage" {
		return h.automations.ExecuteControl(action, p, automationInvocation(e), func(digest string) bool {
			return contracts.ConsumeToolApproval(e, contracts.ToolApprovalFingerprint(e, tool, action, digest))
		})
	}
	limit, offset := 20, 0
	for key, dest := range map[string]*int{"limit": &limit, "offset": &offset} {
		if v, ok := p[key].(float64); ok {
			if v != float64(int(v)) || v < 0 || key == "limit" && (v < 1 || v > 100) {
				return nil, fmt.Errorf("invalid %s", key)
			}
			*dest = int(v)
		}
	}
	switch action {
	case "list":
		list, err := h.automations.ListAutomations(api.AutomationListRequest{})
		if err != nil {
			return nil, err
		}
		if offset > len(list.Items) {
			offset = len(list.Items)
		}
		end := offset + limit
		if end > len(list.Items) {
			end = len(list.Items)
		}
		items := list.Items[offset:end]
		for i := range items {
			items[i].SourceFile = ""
		}
		return map[string]any{"items": items, "total": list.Total, "hasMore": end < len(list.Items), "nextOffset": end, "executionHistory": list.ExecutionHistory}, nil
	case "get":
		return h.automations.ControlGet(stringValue(p, "id"))
	case "executions":
		result, err := h.automations.ListAutomationExecutions(api.AutomationExecutionsRequest{ID: stringValue(p, "id"), Limit: limit, Offset: offset})
		for i := range result.Items {
			result.Items[i].SourceFile = ""
		}
		return result, err
	case "execution":
		result, err := h.automations.LoadAutomationExecution(api.AutomationExecutionRequest{ExecutionID: stringValue(p, "executionId")})
		result.SourceFile = ""
		return result, err
	case "validate":
		plan, err := h.automations.PrepareControl("create", p, "")
		if err != nil {
			return map[string]any{"valid": false, "diagnostics": []any{candidateError("invalid_automation", err)}}, nil
		}
		return map[string]any{"valid": true, "automation": plan.After, "preview": plan.Preview}, nil
	}
	return nil, fmt.Errorf("unsupported automation action")
}
