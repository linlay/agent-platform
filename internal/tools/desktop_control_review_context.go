package tools

import (
	"context"
	"strings"
	"time"

	. "agent-platform/internal/contracts"
)

// Review context is a best-effort display snapshot, not a new authority or a
// mutation baseline. Only fixed read actions run; diagnostics are never prefetched.
func (t *RuntimeToolExecutor) desktopReviewContext(ctx context.Context, action string, params map[string]any, e *ExecutionContext) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out := map[string]any{}
	read := func(action string, args map[string]any) map[string]any {
		if ctx.Err() != nil {
			return nil
		}
		result, err := t.dispatchDesktopAction(ctx, "desktop."+action, map[string]any{"args": args}, e)
		if err != nil || result.ExitCode != 0 || result.Error != "" {
			return nil
		}
		response, _ := result.Structured["response"].(map[string]any)
		if response["ok"] != true {
			return nil
		}
		value, _ := response["result"].(map[string]any)
		return value
	}
	current := func(value map[string]any, keys ...string) {
		if len(value) == 0 {
			return
		}
		selected := map[string]any{}
		for _, key := range keys {
			if v, ok := value[key]; ok {
				selected[key] = v
			}
		}
		if len(selected) > 0 {
			out["current"] = selected
			out["capturedAt"] = time.Now().UTC().Format(time.RFC3339)
		}
	}
	first := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := params[key].(string); ok && strings.TrimSpace(value) != "" {
				return value
			}
		}
		return ""
	}
	names := func(value map[string]any, list, key, label string, ids ...string) {
		selected, _ := out["names"].(map[string]any)
		if selected == nil {
			selected = map[string]any{}
		}
		for _, id := range ids {
			if item := desktopReviewItem(value, list, key, id); item != nil {
				if v, ok := item[label].(string); ok && v != "" {
					selected[id] = v
				}
			}
		}
		if len(selected) > 0 {
			out["names"] = selected
		}
	}
	switch action {
	case "theme.set":
		current(read("theme.get", nil), "themeMode")
	case "locale.set":
		current(read("locale.get", nil), "locale")
	case "skin.set", "skin.remove":
		state := read("skin.get", nil)
		current(state, "skinId", "customBackground")
		names(read("skin.list", nil), "skins", "skinId", "name", first("skinId"), AnyStringNode(state["skinId"]))
	case "pet.show", "pet.hide", "pet.set":
		state := read("pet.state", nil)
		current(state, "enabled", "appearanceId")
		names(read("pet.list", nil), "appearances", "id", "displayName", first("appearanceId", "id"), AnyStringNode(state["appearanceId"]))
	case "copilot.setPagePreference":
		data := read("copilot.getPagePreferences", nil)
		pages, _ := data["desktopCopilotPages"].(map[string]any)
		page, _ := pages[first("pageKey")].(map[string]any)
		current(page, "enabled", "agentKey")
		names(data, "agentOptions", "value", "label", first("agentKey"), AnyStringNode(page["agentKey"]))
	case "website.update", "website.remove":
		item := desktopReviewItem(read("website.list", nil), "items", "id", first("websiteId", "id"))
		current(item, "label", "url", "copilotAgentKey")
	case "kanban.updateIssue", "kanban.deleteIssue", "kanban.moveIssue":
		if id := first("id"); id != "" {
			data := read("kanban.getIssue", map[string]any{"id": id})
			issue, _ := data["issue"].(map[string]any)
			if issue["id"] == id {
				keys := []string{"title", "status", "automationId", "automationEnabled", "assigneeAgentKey"}
				// Preserve only the fields being changed, not the rest of the issue record.
				input, _ := params["input"].(map[string]any)
				for _, key := range desktopKanbanReviewFields {
					if _, ok := input[key]; ok {
						keys = append(keys, key)
					}
				}
				current(issue, keys...)
			}
		}
	case "web.exportArtifact":
		if id := first("surfaceId"); id != "" {
			data := read("web.getSurfaceState", map[string]any{"surfaceId": id})
			surface, _ := data["surface"].(map[string]any)
			if surface["surfaceId"] == id {
				current(surface, "title", "url")
			}
		}
	case "webapp.start", "webapp.stop", "webapp.restart", "webapp.open", "webapp.updatePreferences", "webapp.unpublish":
		id := first("webappId", "id")
		item := desktopReviewItem(read("site.list", nil), "items", "id", id)
		if item["kind"] == "webapp" {
			current(item, "label", "openMode")
		}
		// site.list contains backend configuration; never retain that whole response.
	}
	if action == "kanban.createIssue" || action == "kanban.updateIssue" {
		input, _ := params["input"].(map[string]any)
		prior, _ := out["current"].(map[string]any)
		if AnyStringNode(input["projectId"]) != "" || AnyStringNode(input["localWorkflowId"]) != "" {
			data := read("kanban.listIssues", nil)
			names(data, "projects", "id", "name", AnyStringNode(input["projectId"]), AnyStringNode(prior["projectId"]))
			names(data, "localWorkflows", "id", "name", AnyStringNode(input["localWorkflowId"]), AnyStringNode(prior["localWorkflowId"]))
		}
		if AnyStringNode(input["assigneeAgentKey"]) != "" {
			names(read("copilot.getPagePreferences", nil), "agentOptions", "value", "label", AnyStringNode(input["assigneeAgentKey"]), AnyStringNode(prior["assigneeAgentKey"]))
		}
	}
	return out
}

func desktopReviewItem(data map[string]any, list, key, id string) map[string]any {
	if id == "" {
		return nil
	}
	items, _ := data[list].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item[key] == id {
			return item
		}
	}
	return nil
}

// Keep snapshots limited to public issue input fields, even for unexpected
// model-supplied keys. Unknown requested fields remain visible in technical details.
var desktopKanbanReviewFields = []string{
	"title", "description", "status", "priority", "severity", "assigneeAgentKey",
	"projectId", "localWorkflowId", "projectVersion", "dueDate", "resolution",
	"securityLevelKey", "reporterId", "componentKeys", "originalEstimate", "remainingEstimate", "timeSpent",
	"assigneeId", "workerType", "workerId", "workerAgent", "runState", "automationId", "automationEnabled",
	"automationCron", "automationMessage", "automationTimezone", "attachmentChatId", "attachments",
	"syncToCloud", "localWorkflowAction", "chatId", "runId",
}
