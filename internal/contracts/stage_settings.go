package contracts

import "strings"

type StageSettings struct {
	SystemPrompt       string
	ModelKey           string
	ProviderKey        string
	Model              string
	Protocol           string
	Tools              []string
	ReasoningEnabled   bool
	ReasoningEffort    string
	DeepThinking       bool
	InstructionsPrompt string
	MaxTokens          int
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
	if settings.Execute.IsZero() {
		settings.Execute = parseStageSettings(raw)
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
	if value := anyIntNode(raw["maxSteps"]); value > 0 {
		settings.MaxSteps = value
	}
	if value := anyIntNode(raw["maxWorkRoundsPerTask"]); value > 0 {
		settings.MaxWorkRoundsPerTask = value
	}
	return settings
}

func parseStageSettings(raw map[string]any) StageSettings {
	return StageSettings{
		SystemPrompt:       anyStringNode(raw["systemPrompt"]),
		ModelKey:           anyStringNode(raw["modelKey"]),
		ProviderKey:        anyStringNode(raw["providerKey"]),
		Model:              anyStringNode(raw["model"]),
		Protocol:           strings.ToUpper(anyStringNode(raw["protocol"])),
		Tools:              anyListStrings(raw["tools"]),
		ReasoningEnabled:   anyBoolNode(raw["reasoningEnabled"]),
		ReasoningEffort:    anyStringNode(raw["reasoningEffort"]),
		DeepThinking:       anyBoolNode(raw["deepThinking"]),
		InstructionsPrompt: anyStringNode(raw["instructionsPrompt"]),
		MaxTokens:          anyIntNode(raw["maxTokens"]),
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
		!s.ReasoningEnabled &&
		strings.TrimSpace(s.ReasoningEffort) == "" &&
		!s.DeepThinking &&
		strings.TrimSpace(s.InstructionsPrompt) == "" &&
		s.MaxTokens == 0
}
