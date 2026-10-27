package contracts

import "strings"

type StageSettings struct {
	SystemPrompt       string
	ModelKey           string
	ProviderKey        string
	Model              string
	Protocol           string
	Tools              []string
	Sampling           SamplingSettings
	ReasoningEnabled   bool
	ReasoningEffort    string
	DeepThinking       bool
	InstructionsPrompt string
	MaxOutputTokens    int
}

// PlanningModeSettings holds the stage settings of a native Agent: Planning
// for a planningMode Run, Execute for every other Run (including the one that
// executes a confirmed plan), and Summary for context-summary calls.
type PlanningModeSettings struct {
	Planning StageSettings
	Execute  StageSettings
	Summary  StageSettings
	MaxSteps int
}

func ResolvePlanningModeSettings(raw map[string]any, defaultMaxSteps int) PlanningModeSettings {
	settings := PlanningModeSettings{MaxSteps: defaultMaxSteps}
	if settings.MaxSteps <= 0 {
		settings.MaxSteps = 60
	}
	if len(raw) == 0 {
		return settings
	}
	if nested := anyMapNode(raw["planning"]); len(nested) > 0 {
		settings.Planning = parseStageSettings(nested)
	}
	if nested := anyMapNode(raw["execute"]); len(nested) > 0 {
		settings.Execute = parseStageSettings(nested)
	}
	if nested := anyMapNode(raw["summary"]); len(nested) > 0 {
		settings.Summary = parseStageSettings(nested)
	}
	if settings.Summary.IsZero() {
		settings.Summary = settings.Execute
	}
	return settings
}

func parseStageSettings(raw map[string]any) StageSettings {
	raw = normalizeStageSettingsNode(raw)
	return StageSettings{
		SystemPrompt:       anyStringNode(raw["systemPrompt"]),
		ModelKey:           anyStringNode(raw["modelKey"]),
		ProviderKey:        anyStringNode(raw["providerKey"]),
		Model:              anyStringNode(raw["model"]),
		Protocol:           strings.ToUpper(anyStringNode(raw["protocol"])),
		Tools:              anyListStrings(raw["tools"]),
		Sampling:           ParseSamplingSettings(anyMapNode(raw["sampling"])),
		ReasoningEnabled:   anyBoolNode(raw["reasoningEnabled"]),
		ReasoningEffort:    anyStringNode(raw["reasoningEffort"]),
		DeepThinking:       anyBoolNode(raw["deepThinking"]),
		InstructionsPrompt: anyStringNode(raw["instructionsPrompt"]),
		MaxOutputTokens:    anyIntNode(raw["maxOutputTokens"]),
	}
}

func normalizeStageSettingsNode(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return raw
	}
	normalized := map[string]any{}
	for _, key := range []string{"systemPrompt", "deepThinking", "instructionsPrompt"} {
		if value, exists := raw[key]; exists {
			normalized[key] = value
		}
	}
	if modelConfig := anyMapNode(raw["modelConfig"]); len(modelConfig) > 0 {
		applyStageModelConfig(normalized, modelConfig)
	}
	if toolConfig := anyMapNode(raw["toolConfig"]); len(toolConfig) > 0 {
		if _, exists := toolConfig["tools"]; exists {
			normalized["tools"] = toolConfig["tools"]
		}
	}
	return normalized
}

func applyStageModelConfig(stage map[string]any, modelConfig map[string]any) {
	for _, key := range []string{"modelKey", "providerKey", "model", "protocol", "maxOutputTokens"} {
		if _, exists := modelConfig[key]; exists {
			stage[key] = modelConfig[key]
		}
	}
	if _, exists := modelConfig["sampling"]; exists {
		stage["sampling"] = modelConfig["sampling"]
	}
	if reasoning := anyMapNode(modelConfig["reasoning"]); len(reasoning) > 0 {
		if _, exists := reasoning["enabled"]; exists {
			stage["reasoningEnabled"] = reasoning["enabled"]
		}
		if _, exists := reasoning["effort"]; exists {
			stage["reasoningEffort"] = reasoning["effort"]
		}
	}
}

func (s StageSettings) PrimaryPrompt() string {
	if strings.TrimSpace(s.InstructionsPrompt) != "" {
		return strings.TrimSpace(s.InstructionsPrompt)
	}
	return strings.TrimSpace(s.SystemPrompt)
}

func (s StageSettings) IsZero() bool {
	return strings.TrimSpace(s.SystemPrompt) == "" &&
		strings.TrimSpace(s.ModelKey) == "" &&
		strings.TrimSpace(s.ProviderKey) == "" &&
		strings.TrimSpace(s.Model) == "" &&
		strings.TrimSpace(s.Protocol) == "" &&
		len(s.Tools) == 0 &&
		s.Sampling.IsZero() &&
		!s.ReasoningEnabled &&
		strings.TrimSpace(s.ReasoningEffort) == "" &&
		!s.DeepThinking &&
		strings.TrimSpace(s.InstructionsPrompt) == "" &&
		s.MaxOutputTokens == 0
}
