package general

import (
	"strings"

	"agent-platform/internal/contracts"
)

type CreateDefaults struct {
	ModelKey        string
	ReasoningEffort string
	Budget          map[string]any
}

// ApplyCreateDefaults fills only the values the caller left empty. Unlike the
// CODER and KBASE types, a general agent has no type-owned icon or visibility.
func ApplyCreateDefaults(definition map[string]any, defaults CreateDefaults) map[string]any {
	if definition == nil {
		return nil
	}
	modelKey := strings.TrimSpace(defaults.ModelKey)
	reasoningEffort := strings.TrimSpace(defaults.ReasoningEffort)
	out := contracts.CloneMap(definition)
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
