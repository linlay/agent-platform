package llm

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"agent-platform-runner-go/internal/api"
	contracts "agent-platform-runner-go/internal/contracts"
)

func normalizeHITLSubmit(args map[string]any, params any) (map[string]any, error) {
	mode := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(args["mode"])))
	switch mode {
	case "approval":
		return normalizeHITLApprovalSubmit(args, params)
	case "form":
		return normalizeHITLFormSubmit(args, params)
	default:
		return nil, fmt.Errorf("unsupported bash HITL mode: %s", mode)
	}
}

func normalizeHITLApprovalSubmit(args map[string]any, params any) (map[string]any, error) {
	items, err := decodeHITLSubmitItems(params)
	if err != nil {
		return nil, fmt.Errorf("bash HITL approval submit params must be an array")
	}
	if len(items) == 0 {
		return map[string]any{
			"mode":      "approval",
			"cancelled": true,
			"reason":    "user_dismissed",
		}, nil
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
			log.Printf("[llm][hitl][warn] approval submit id mismatch index=%d expected=%s actual=%s",
				index,
				definitionID,
				submittedID,
			)
		}
		decision := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["decision"])))
		switch decision {
		case "approve", "reject", "approve_always":
		default:
			return nil, fmt.Errorf("items[%d]: decision must be approve, reject, or approve_always", index)
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
		"approvals": approvals,
	}, nil
}

func normalizeHITLFormSubmit(args map[string]any, params any) (map[string]any, error) {
	items, err := decodeHITLSubmitItems(params)
	if err != nil {
		return nil, fmt.Errorf("bash HITL form submit params must be an array")
	}
	if len(items) == 0 {
		return map[string]any{
			"mode":      "form",
			"cancelled": true,
			"reason":    "user_dismissed",
		}, nil
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
		if rawPayload, exists := item["payload"]; exists {
			payload := contracts.AnyMapNode(rawPayload)
			if payload == nil {
				return nil, fmt.Errorf("items[%d]: payload must be an object", index)
			}
			if _, hasReason := item["reason"]; hasReason {
				return nil, fmt.Errorf("items[%d]: payload and reason cannot both be provided", index)
			}
			entry["action"] = "submit"
			entry["payload"] = payload
		} else if reason := strings.TrimSpace(contracts.AnyStringNode(item["reason"])); reason != "" {
			entry["action"] = "reject"
			entry["reason"] = reason
		} else {
			entry["action"] = "cancel"
		}
		forms = append(forms, entry)
	}

	return map[string]any{
		"mode":  "form",
		"forms": forms,
	}, nil
}

func decodeHITLSubmitItems(params any) ([]map[string]any, error) {
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
