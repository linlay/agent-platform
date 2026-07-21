package kbase

import (
	"strings"

	"agent-platform/internal/contracts"
)

type CreateDefaults struct {
	ModelKey          string
	ReasoningEffort   string
	EmbeddingModelKey string
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

	embeddingModelKey := strings.TrimSpace(defaults.EmbeddingModelKey)
	kbaseConfig := contracts.CloneMap(contracts.AnyMapNode(out["kbaseConfig"]))
	if kbaseConfig == nil {
		kbaseConfig = map[string]any{}
	}
	embedding := contracts.CloneMap(contracts.AnyMapNode(kbaseConfig["embedding"]))
	if embedding == nil {
		embedding = map[string]any{}
	}
	explicitModelKey := strings.TrimSpace(contracts.AnyStringNode(embedding["modelKey"]))
	if explicitModelKey != "" || embeddingModelKey != "" {
		if explicitModelKey == "" {
			explicitModelKey = embeddingModelKey
		}
		embedding["modelKey"] = explicitModelKey
		kbaseConfig["embedding"] = embedding
		out["kbaseConfig"] = kbaseConfig
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
