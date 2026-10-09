package hitl

import (
	"fmt"
	"log"
	"strings"

	contracts "agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/hitl/planning"
)

func Normalize(args map[string]any, params any) (map[string]any, error) {
	mode := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(args["mode"])))
	switch mode {
	case "approval":
		return NormalizeApproval(args, params)
	case "form":
		return NormalizeForm(args, params)
	case "planning":
		return NormalizePlanningConfirmation(args, params)
	default:
		return nil, fmt.Errorf("unsupported bash HITL mode: %s", mode)
	}
}

func NormalizeApproval(args map[string]any, params any) (map[string]any, error) {
	items, err := queryinput.DecodeSubmitItems(params)
	if err != nil {
		return nil, fmt.Errorf("bash HITL approval submit params must be an array")
	}
	if len(items) == 0 {
		return contracts.AwaitingErrorAnswer("approval", "user_dismissed", "用户关闭等待项"), nil
	}

	definitions := cloneAnySlice(args["approvals"])
	if len(items) != len(definitions) {
		return nil, fmt.Errorf("expected %d approvals, got %d", len(definitions), len(items))
	}

	approvals := make([]map[string]any, 0, len(items))
	for index, item := range items {
		definition := contracts.AnyMapNode(definitions[index])
		definitionID := contracts.AnyStringNode(definition["id"])
		submittedID := contracts.AnyStringNode(item["id"])
		if submittedID != "" && definitionID != "" && submittedID != definitionID {
			log.Printf("[hitl][warn] approval submit id mismatch index=%d expected=%s actual=%s",
				index,
				definitionID,
				submittedID,
			)
		}
		decision := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["decision"])))
		if decision == "" {
			return nil, fmt.Errorf("items[%d]: decision is required", index)
		}
		switch decision {
		case "approve", "approve_rule_run", "reject":
		default:
			return nil, fmt.Errorf("items[%d]: unsupported approval decision %q", index, decision)
		}
		if decision == "approve_rule_run" {
			if raw, exists := definition["options"]; exists {
				allowed := false
				for _, option := range cloneAnySlice(raw) {
					if contracts.AnyStringNode(contracts.AnyMapNode(option)["decision"]) == decision {
						allowed = true
					}
				}
				if !allowed {
					return nil, fmt.Errorf("items[%d]: run approval is not offered for this requirement", index)
				}
			}
		}
		entry := map[string]any{
			"id":       definitionID,
			"command":  contracts.AnyStringNode(definition["command"]),
			"decision": decision,
		}
		if reason := strings.TrimSpace(contracts.AnyStringNode(item["reason"])); reason != "" {
			entry["reason"] = reason
		}
		approvals = append(approvals, entry)
	}

	return map[string]any{
		"mode":      "approval",
		"status":    "answered",
		"approvals": approvals,
	}, nil
}

func NormalizeForm(args map[string]any, param any) (map[string]any, error) {
	item, err := queryinput.DecodeSubmitSingle(param)
	if err != nil {
		return nil, fmt.Errorf("form submit requires param: %w", err)
	}
	definition := contracts.AnyMapNode(args["form"])
	entry := map[string]any{}
	if command := contracts.AnyStringNode(definition["command"]); command != "" {
		entry["command"] = command
	}
	data, hasData := item["data"].(map[string]any)
	if _, present := item["data"]; present && (!hasData || data == nil) {
		return nil, fmt.Errorf("param.data must be an object")
	}
	switch decision := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["decision"]))); decision {
	case "dismiss":
		return contracts.AwaitingErrorAnswer("form", "user_dismissed", "用户关闭等待项"), nil
	case "approve":
		if !hasData {
			return nil, fmt.Errorf("param.data is required for approve")
		}
		entry["decision"] = "approve"
		entry["data"] = data
	case "reject":
		entry["decision"] = "reject"
		if reason := strings.TrimSpace(contracts.AnyStringNode(item["reason"])); reason != "" {
			entry["reason"] = reason
		}
		if len(data) > 0 {
			entry["data"] = data
		}
	case "":
		return nil, fmt.Errorf("param.decision is required")
	default:
		return nil, fmt.Errorf("unsupported form decision %q", decision)
	}
	return map[string]any{
		"mode":   "form",
		"status": "answered",
		"form":   entry,
	}, nil
}

func NormalizePlanningConfirmation(args map[string]any, params any) (map[string]any, error) {
	return planning.NormalizeConfirmation(args, params)
}

func cloneAnySlice(value any) []any {
	items, _ := value.([]any)
	if len(items) == 0 {
		return nil
	}
	cloned := make([]any, 0, len(items))
	for _, item := range items {
		if mapped := contracts.AnyMapNode(item); len(mapped) > 0 {
			cloned = append(cloned, contracts.CloneMap(mapped))
			continue
		}
		cloned = append(cloned, item)
	}
	return cloned
}
