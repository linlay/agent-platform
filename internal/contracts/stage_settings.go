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

type PlanExecuteSettings struct {
	Plan                 StageSettings
	Execute              StageSettings
	Summary              StageSettings
	TaskExecutionPrompt  string
	MaxSteps             int
	MaxWorkRoundsPerTask int
}

func ResolvePlanExecuteSettings(raw map[string]any, defaultsMaxSteps int, defaultsMaxWorkRounds int) PlanExecuteSettings {
	settings := PlanExecuteSettings{
		MaxSteps:             defaultsMaxSteps,
		MaxWorkRoundsPerTask: defaultsMaxWorkRounds,
	}
	if settings.MaxSteps <= 0 {
		settings.MaxSteps = 60
	}
	if settings.MaxWorkRoundsPerTask <= 0 {
		settings.MaxWorkRoundsPerTask = 6
	}
	if len(raw) == 0 {
		return settings
	}
	if nested := anyMapNode(raw["plan"]); len(nested) > 0 {
		settings.Plan = parseStageSettings(nested)
	}
	if nested := anyMapNode(raw["execute"]); len(nested) > 0 {
		settings.Execute = parseStageSettings(nested)
	}
	if nested := anyMapNode(raw["summary"]); len(nested) > 0 {
		settings.Summary = parseStageSettings(nested)
	}
	if settings.Plan.IsZero() {
		settings.Plan = settings.Execute
	}
	if settings.Summary.IsZero() {
		settings.Summary = settings.Execute
	}
	if value := anyStringNode(raw["taskExecutionPromptTemplate"]); value != "" {
		settings.TaskExecutionPrompt = value
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
