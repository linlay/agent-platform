package plan

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func Normalize(args map[string]any, params any) (map[string]any, error) {
	items, err := decodeItems(params)
	if err != nil {
		return nil, fmt.Errorf("plan submit params must be an array")
	}
	if len(items) == 0 {
		return contracts.AwaitingErrorAnswer("plan", "user_dismissed", "用户关闭等待项"), nil
	}
	if len(items) != 1 {
		return nil, fmt.Errorf("expected 1 plan, got %d", len(items))
	}

	definition := contracts.AnyMapNode(args["plan"])
	if len(definition) == 0 {
		return nil, fmt.Errorf("plan definition is required")
	}
	item := items[0]
	definitionID := strings.TrimSpace(contracts.AnyStringNode(definition["id"]))
	submittedID := strings.TrimSpace(contracts.AnyStringNode(item["id"]))
	if submittedID != "" && definitionID != "" && submittedID != definitionID {
		log.Printf("[hitl][warn] plan submit id mismatch expected=%s actual=%s", definitionID, submittedID)
	}
	decision := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["decision"])))
	if decision == "" {
		return nil, fmt.Errorf("items[0]: decision is required")
	}
	switch decision {
	case "approve", "reject":
	default:
		return nil, fmt.Errorf("items[0]: unsupported plan decision %q", decision)
	}

	entry := map[string]any{
		"id":           firstNonBlank(definitionID, submittedID),
		"planningId":   strings.TrimSpace(contracts.AnyStringNode(definition["planningId"])),
		"planningFile": strings.TrimSpace(contracts.AnyStringNode(definition["planningFile"])),
		"decision":     decision,
	}
	if reason := strings.TrimSpace(contracts.AnyStringNode(item["reason"])); reason != "" {
		entry["reason"] = reason
	}
	return map[string]any{
		"mode":   "plan",
		"status": "answered",
		"plan":   entry,
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

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
