package contracts

import "strings"

// ApprovalSummary deliberately excludes commands, arguments and arbitrary payloads.
type ApprovalSummary struct {
	ToolName    string `json:"toolName,omitempty"`
	Description string `json:"description,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// SummarizeApprovals retains at most three bounded, display-only summaries.
func SummarizeApprovals(value any) ([]ApprovalSummary, bool) {
	var items []any
	switch values := value.(type) {
	case []any:
		items = values
	case []map[string]any:
		for _, item := range values {
			items = append(items, item)
		}
	}
	truncated := len(items) > 3
	if len(items) > 3 {
		items = items[:3]
	}
	clip := func(value any) string {
		text, _ := value.(string)
		chars := []rune(strings.TrimSpace(text))
		if len(chars) > 200 {
			chars = chars[:200]
			truncated = true
		}
		return string(chars)
	}
	var result []ApprovalSummary
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		reason := clip(item["reason"])
		if policy, ok := item["policy"].(map[string]any); ok && reason == "" {
			reason = clip(policy["reason"])
		}
		if requirements, ok := item["requirements"].([]any); ok {
			for _, raw := range requirements {
				requirement, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				part := clip(requirement["reason"])
				if part != "" && part != reason {
					if reason != "" {
						reason += "; "
					}
					reason += part
				}
			}
		}
		if reason == "" {
			reason = clip(item["ruleKey"])
		}
		result = append(result, ApprovalSummary{ToolName: clip(item["toolName"]), Description: clip(item["description"]), Reason: clip(reason)})
	}
	return result, truncated
}
