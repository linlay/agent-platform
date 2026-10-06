package kbase

import (
	"strings"

	"agent-platform/internal/contracts"
)

type CreateDefaults struct {
	ModelKey        string
	ReasoningEffort string
}

func ApplyCreateDefaults(definition map[string]any, defaults CreateDefaults) map[string]any {
	if definition == nil {
		return nil
	}
	out := contracts.CloneMap(definition)
	if emptyCreateValue(out["icon"]) {
		out["icon"] = map[string]any{"name": DefaultIconName}
	}
	visibility := contracts.CloneMap(contracts.AnyMapNode(out["visibility"]))
	if visibility == nil {
		visibility = map[string]any{}
	}
	if len(createStrings(visibility["scopes"])) == 0 {
		visibility["scopes"] = []any{"nav"}
		out["visibility"] = visibility
	}

	modelKey := strings.TrimSpace(defaults.ModelKey)
	reasoningEffort := strings.TrimSpace(defaults.ReasoningEffort)
	if modelKey != "" || reasoningEffort != "" {
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
		out["modelConfig"] = modelConfig
	}

	return out
}

// ApplyCreateToolDefaults writes the creation tool list when the caller did
// not send toolConfig.tools at all. An explicit list, including an empty one,
// is kept as is. It runs only on creation, never when an existing agent is
// loaded or saved.
func ApplyCreateToolDefaults(definition map[string]any) map[string]any {
	if definition == nil {
		return nil
	}
	toolConfig := contracts.AnyMapNode(definition["toolConfig"])
	if _, declared := toolConfig["tools"]; declared {
		return definition
	}
	out := contracts.CloneMap(definition)
	toolConfig = contracts.CloneMap(toolConfig)
	if toolConfig == nil {
		toolConfig = map[string]any{}
	}
	names := CreateToolNames()
	tools := make([]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, name)
	}
	toolConfig["tools"] = tools
	out["toolConfig"] = toolConfig
	return out
}

func emptyCreateValue(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

func createStrings(value any) []string {
	var raw []any
	switch typed := value.(type) {
	case []any:
		raw = typed
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}
