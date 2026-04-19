package llm

import (
	"encoding/json"
	"fmt"
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

	definitions := make(map[string]map[string]any)
	for _, rawApproval := range cloneAnySlice(args["approvals"]) {
		approval := contracts.AnyMapNode(rawApproval)
		id := strings.TrimSpace(contracts.AnyStringNode(approval["id"]))
		if id == "" {
			continue
		}
		definitions[id] = approval
	}

	approvals := make([]map[string]any, 0, len(items))
	seenIDs := map[string]bool{}
	for _, item := range items {
		id := strings.TrimSpace(contracts.AnyStringNode(item["id"]))
		if id == "" {
			return nil, fmt.Errorf("bash HITL approval answers.id is required")
		}
		if seenIDs[id] {
			return nil, fmt.Errorf("duplicate approval id: %s", id)
		}
		definition, ok := definitions[id]
		if !ok {
			return nil, fmt.Errorf("unknown approval id: %s", id)
		}
		decision := strings.ToLower(strings.TrimSpace(contracts.AnyStringNode(item["decision"])))
		switch decision {
		case "approve", "reject", "approve_always":
		default:
			return nil, fmt.Errorf("%s: decision must be approve, reject, or approve_always", id)
		}
		entry := map[string]any{
			"id":       id,
			"command":  contracts.AnyStringNode(definition["command"]),
			"decision": decision,
		}
		if reason := strings.TrimSpace(contracts.AnyStringNode(item["reason"])); reason != "" {
			entry["reason"] = reason
		}
		approvals = append(approvals, entry)
		seenIDs[id] = true
	}
	if len(approvals) != len(definitions) {
		return nil, fmt.Errorf("expected %d approvals, got %d", len(definitions), len(approvals))
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

	definitions := make(map[string]map[string]any)
	for _, rawForm := range cloneAnySlice(args["forms"]) {
		form := contracts.AnyMapNode(rawForm)
		id := strings.TrimSpace(contracts.AnyStringNode(form["id"]))
		if id == "" {
			continue
		}
		definitions[id] = form
	}

	forms := make([]map[string]any, 0, len(items))
	seenIDs := map[string]bool{}
	for _, item := range items {
		id := strings.TrimSpace(contracts.AnyStringNode(item["id"]))
		if id == "" {
			return nil, fmt.Errorf("bash HITL form answers.id is required")
		}
		if seenIDs[id] {
			return nil, fmt.Errorf("duplicate form id: %s", id)
		}
		definition, ok := definitions[id]
		if !ok {
			return nil, fmt.Errorf("unknown form id: %s", id)
		}
		entry := map[string]any{
			"id":      id,
			"command": contracts.AnyStringNode(definition["command"]),
		}
		if rawPayload, exists := item["payload"]; exists {
			payload := contracts.AnyMapNode(rawPayload)
			if payload == nil {
				return nil, fmt.Errorf("%s: payload must be an object", id)
			}
			if _, hasReason := item["reason"]; hasReason {
				return nil, fmt.Errorf("%s: payload and reason cannot both be provided", id)
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
		seenIDs[id] = true
	}
	if len(forms) != len(definitions) {
		return nil, fmt.Errorf("expected %d forms, got %d", len(definitions), len(forms))
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
