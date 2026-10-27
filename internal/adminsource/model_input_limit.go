package adminsource

import (
	"agent-platform/internal/catalog"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
)

// ValidateModelInputLimit belongs at management entry points: a configured
// modelConfig.maxInputTokens must not exceed the limit of the Agent's model.
// Field shape and non-native Agents are left to the catalog validation.
func ValidateModelInputLimit(definition map[string]any, registry *models.ModelRegistry) error {
	modelConfig := contracts.AnyMapNode(definition["modelConfig"])
	limit, err := catalog.ParseAgentMaxInputTokens(modelConfig)
	if err != nil || limit == 0 {
		return err
	}
	mode, engine, err := catalog.ParseAgentModeAndEngine(contracts.AnyStringNode(definition["mode"]), contracts.AnyStringNode(definition["engine"]))
	if err != nil || engine == catalog.AgentEngineACP || catalog.AgentIsProxyMode(mode) || catalog.AgentIsChannelMode(mode) {
		return nil
	}
	modelKey := contracts.AnyStringNode(modelConfig["modelKey"])
	if modelKey == "" {
		return nil
	}
	return registry.ValidateAgentMaxInputTokens(modelKey, limit)
}
