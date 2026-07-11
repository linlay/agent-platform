package coder

import (
	"strings"

	"agent-platform/internal/contracts"
)

type CreateDefaults struct {
	ModelKey        string
	ReasoningEffort string
	Budget          map[string]any
}

func ApplyCreateDefaults(definition map[string]any, defaults CreateDefaults) map[string]any {
	if definition == nil {
		return nil
	}
	modelKey := strings.TrimSpace(defaults.ModelKey)
	reasoningEffort := strings.TrimSpace(defaults.ReasoningEffort)
	out := contracts.CloneMap(definition)
	if emptyCreateValue(out["icon"]) {
		out["icon"] = map[string]any{"name": DefaultIconName}
	}
	out["visibility"] = map[string]any{"scopes": normalizeCreateVisibility(contracts.AnyMapNode(out["visibility"])["scopes"])}
	if _, exists := out["budget"]; !exists && len(defaults.Budget) > 0 {
		out["budget"] = contracts.CloneMap(defaults.Budget)
	}
	modelConfig := contracts.CloneMap(contracts.AnyMapNode(out["modelConfig"]))
	if modelConfig == nil {
		modelConfig = map[string]any{}
	}
	if modelKey != "" && strings.TrimSpace(contracts.AnyStringNode(modelConfig["modelKey"])) == "" {
		modelConfig["modelKey"] = modelKey
	}
	if reasoningEffort != "" {
		reasoning := contracts.CloneMap(contracts.AnyMapNode(modelConfig["reasoning"]))
		if reasoning == nil {
			reasoning = map[string]any{}
		}
		if strings.TrimSpace(contracts.AnyStringNode(reasoning["effort"])) == "" {
			reasoning["effort"] = reasoningEffort
		}
		modelConfig["reasoning"] = reasoning
	}
	if len(modelConfig) > 0 {
		out["modelConfig"] = modelConfig
	}
	return out
}

func emptyCreateValue(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

func normalizeCreateVisibility(value any) []any {
	var raw []string
	switch typed := value.(type) {
	case []string:
		raw = typed
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				raw = append(raw, text)
			}
		}
	}
	seen := map[string]struct{}{}
	scopes := make([]any, 0, len(raw))
	hasPrivate := false
	for _, scope := range raw {
		scope = strings.ToLower(strings.TrimSpace(scope))
		switch scope {
		case "nav", "copilot", "invoke", "internal":
		default:
			continue
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, scope)
		if scope == "invoke" || scope == "internal" {
			hasPrivate = true
		}
	}
	if !hasPrivate {
		return []any{"nav"}
	}
	return scopes
}
