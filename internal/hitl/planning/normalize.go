package planning

import (
	"fmt"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
)

func NormalizeConfirmation(args map[string]any, param any) (map[string]any, error) {
	item, err := queryinput.DecodeSubmitSingle(param)
	if err != nil {
		return nil, fmt.Errorf("planning confirmation submit requires param: %w", err)
	}
	definition := contracts.AnyMapNode(args["planning"])
	if len(definition) == 0 {
		return nil, fmt.Errorf("planning confirmation definition is required")
	}
	if _, hasData := item["data"]; hasData {
		return nil, fmt.Errorf("planning confirmation does not allow data")
	}
	decision := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["decision"])))
	switch decision {
	case "dismiss":
		return contracts.AwaitingErrorAnswer("planning", "user_dismissed", "用户关闭等待项"), nil
	case "approve", "reject":
	case "":
		return nil, fmt.Errorf("param.decision is required")
	default:
		return nil, fmt.Errorf("unsupported planning confirmation decision %q", decision)
	}

	entry := map[string]any{
		"planningId":   strings.TrimSpace(contracts.AnyStringNode(definition["planningId"])),
		"planningFile": strings.TrimSpace(contracts.AnyStringNode(definition["planningFile"])),
		"decision":     decision,
	}
	if reason := strings.TrimSpace(contracts.AnyStringNode(item["reason"])); reason != "" {
		entry["reason"] = reason
	}
	return map[string]any{
		"mode":     "planning",
		"status":   "answered",
		"planning": entry,
	}, nil
}
