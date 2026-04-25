package hitlsubmit

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"agent-platform-runner-go/internal/api"
	contracts "agent-platform-runner-go/internal/contracts"
)

func Normalize(args map[string]any, params any) (map[string]any, error) {
	mode := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(args["mode"])))
	switch mode {
	case "approval":
		return NormalizeApproval(args, params)
	case "form":
		return NormalizeForm(args, params)
	default:
		return nil, fmt.Errorf("unsupported bash HITL mode: %s", mode)
	}
}

func NormalizeApproval(args map[string]any, params any) (map[string]any, error) {
	items, err := decodeItems(params)
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
			log.Printf("[hitlsubmit][warn] approval submit id mismatch index=%d expected=%s actual=%s",
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
		case "approve", "approve_prefix_run":
		case "reject":
		default:
			log.Printf("[hitlsubmit][warn] unknown approval decision index=%d decision=%s; normalizing to reject",
				index,
				decision,
			)
			decision = "reject"
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

func NormalizeForm(args map[string]any, params any) (map[string]any, error) {
	items, err := decodeItems(params)
	if err != nil {
		return nil, fmt.Errorf("bash HITL form submit params must be an array")
	}
	if len(items) == 0 {
		return contracts.AwaitingErrorAnswer("form", "user_dismissed", "用户关闭等待项"), nil
	}

	definitions := cloneAnySlice(args["forms"])
	if len(items) != len(definitions) {
		return nil, fmt.Errorf("expected %d forms, got %d", len(definitions), len(items))
	}

	forms := make([]map[string]any, 0, len(items))
	for index, item := range items {
		definition := contracts.AnyMapNode(definitions[index])
		entryID := contracts.AnyStringNode(definition["id"])
		entry := map[string]any{
			"id":      entryID,
			"command": contracts.AnyStringNode(definition["command"]),
		}
		action := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["action"])))
		if action == "" {
			return nil, fmt.Errorf("items[%d]: action is required", index)
		}
		switch action {
		case "submit":
			form := contracts.AnyMapNode(item["form"])
			if form == nil {
				return nil, fmt.Errorf("items[%d]: form is required for submit", index)
			}
			entry["action"] = "submit"
			entry["form"] = form
		case "reject", "cancel":
			entry["action"] = action
		default:
			return nil, fmt.Errorf("items[%d]: unsupported action %q", index, action)
		}
		forms = append(forms, entry)
	}

	return map[string]any{
		"mode":   "form",
		"status": "answered",
		"forms":  forms,
	}, nil
}

func decodeItems(params any) ([]map[string]any, error) {
	switch typed := params.(type) {
	case api.SubmitParams:
		return api.DecodeSubmitParams(typed)
	case []json.RawMessage:
		return api.DecodeSubmitParams(api.SubmitParams(typed))
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, raw := range typed {
			item := contracts.AnyMapNode(raw)
			if len(item) == 0 {
				return nil, fmt.Errorf("submit items must be objects")
			}
			items = append(items, item)
		}
		return items, nil
	default:
		return nil, fmt.Errorf("submit params must be an array")
	}
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
