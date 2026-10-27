package models

import "fmt"

// DefaultContextWindow is the input window of a model whose registry entry
// declares no maxInputTokens.
const DefaultContextWindow = 128000

// ModelContextWindow is the model's declared input limit, or the default.
func ModelContextWindow(model ModelDefinition) int {
	if model.ContextWindow > 0 {
		return model.ContextWindow
	}
	return DefaultContextWindow
}

// EffectiveContextWindow is the window a Run uses: the Agent's
// modelConfig.maxInputTokens when set, never above the model's limit.
func EffectiveContextWindow(model ModelDefinition, agentMaxInputTokens int) int {
	window := ModelContextWindow(model)
	if agentMaxInputTokens > 0 && agentMaxInputTokens < window {
		return agentMaxInputTokens
	}
	return window
}

// ValidateAgentMaxInputTokens checks an Agent's maxInputTokens against the
// limit of its configured model. Management entry points call it on save.
func (r *ModelRegistry) ValidateAgentMaxInputTokens(modelKey string, agentMaxInputTokens int) error {
	if agentMaxInputTokens <= 0 {
		return nil
	}
	if r == nil {
		return fmt.Errorf("model registry is not configured; cannot verify modelConfig.maxInputTokens")
	}
	model, err := r.GetModel(modelKey)
	if err != nil {
		return err
	}
	if limit := ModelContextWindow(model); agentMaxInputTokens > limit {
		return fmt.Errorf("modelConfig.maxInputTokens %d exceeds model %s limit %d", agentMaxInputTokens, model.Key, limit)
	}
	return nil
}
