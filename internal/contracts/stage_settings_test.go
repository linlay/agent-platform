package contracts

import "testing"

func TestStageSettingsParsesMaxOutputTokens(t *testing.T) {
	settings := parseStageSettings(map[string]any{
		"modelConfig": map[string]any{
			"maxOutputTokens": 8192,
		},
	})

	if settings.MaxOutputTokens != 8192 {
		t.Fatalf("expected maxOutputTokens 8192, got %d", settings.MaxOutputTokens)
	}
	if settings.IsZero() {
		t.Fatal("expected stage settings with maxOutputTokens to be non-zero")
	}
}

func TestStageSettingsParsesSamplingWithZeroTemperature(t *testing.T) {
	settings := parseStageSettings(map[string]any{
		"modelConfig": map[string]any{
			"sampling": map[string]any{
				"temperature":      0,
				"top_p":            0.95,
				"presencePenalty":  0,
				"frequencyPenalty": 0.25,
				"seed":             42,
			},
		},
	})

	if settings.Sampling.Temperature == nil || *settings.Sampling.Temperature != 0 {
		t.Fatalf("expected explicit zero temperature, got %#v", settings.Sampling.Temperature)
	}
	if settings.Sampling.TopP == nil || *settings.Sampling.TopP != 0.95 {
		t.Fatalf("expected topP 0.95, got %#v", settings.Sampling.TopP)
	}
	if settings.Sampling.PresencePenalty == nil || *settings.Sampling.PresencePenalty != 0 {
		t.Fatalf("expected explicit zero presence penalty, got %#v", settings.Sampling.PresencePenalty)
	}
	if settings.Sampling.FrequencyPenalty == nil || *settings.Sampling.FrequencyPenalty != 0.25 {
		t.Fatalf("expected frequency penalty 0.25, got %#v", settings.Sampling.FrequencyPenalty)
	}
	if settings.Sampling.Seed == nil || *settings.Sampling.Seed != 42 {
		t.Fatalf("expected seed 42, got %#v", settings.Sampling.Seed)
	}
	if settings.IsZero() {
		t.Fatal("expected stage settings with sampling to be non-zero")
	}
}

func TestResolvePlanExecuteSettingsIgnoresRootSampling(t *testing.T) {
	settings := ResolvePlanExecuteSettings(map[string]any{
		"sampling": map[string]any{
			"temperature": 0.7,
			"topP":        0.9,
		},
		"plan": map[string]any{
			"modelConfig": map[string]any{
				"sampling": map[string]any{
					"temperature": 0.2,
				},
			},
		},
	}, 0, 0)

	if settings.Plan.Sampling.Temperature == nil || *settings.Plan.Sampling.Temperature != 0.2 {
		t.Fatalf("expected plan temperature override, got %#v", settings.Plan.Sampling)
	}
	if settings.Plan.Sampling.TopP != nil {
		t.Fatalf("did not expect root topP to be inherited, got %#v", settings.Plan.Sampling)
	}
	if !settings.Execute.Sampling.IsZero() || !settings.Summary.Sampling.IsZero() {
		t.Fatalf("did not expect root sampling to create execute/summary settings, got execute=%#v summary=%#v", settings.Execute.Sampling, settings.Summary.Sampling)
	}
}

func TestResolvePlanExecuteSettingsParsesNestedStageModelAndToolConfig(t *testing.T) {
	settings := ResolvePlanExecuteSettings(map[string]any{
		"plan": map[string]any{
			"modelConfig": map[string]any{
				"modelKey": "nested-model",
				"reasoning": map[string]any{
					"enabled": true,
					"effort":  "HIGH",
				},
				"maxOutputTokens": 4096,
				"sampling": map[string]any{
					"temperature": 0.2,
					"topP":        0.9,
				},
			},
			"toolConfig": map[string]any{
				"tools": []any{"file_read", "datetime"},
			},
		},
	}, 0, 0)

	if settings.Plan.ModelKey != "nested-model" {
		t.Fatalf("expected nested model key to win, got %q", settings.Plan.ModelKey)
	}
	if settings.Plan.ReasoningEnabled != true || settings.Plan.ReasoningEffort != "HIGH" {
		t.Fatalf("expected nested reasoning to win, got enabled=%v effort=%q", settings.Plan.ReasoningEnabled, settings.Plan.ReasoningEffort)
	}
	if settings.Plan.MaxOutputTokens != 4096 {
		t.Fatalf("expected nested max output tokens, got %d", settings.Plan.MaxOutputTokens)
	}
	if len(settings.Plan.Tools) != 2 || settings.Plan.Tools[0] != "file_read" || settings.Plan.Tools[1] != "datetime" {
		t.Fatalf("expected nested tools to win, got %#v", settings.Plan.Tools)
	}
	if settings.Plan.Sampling.Temperature == nil || *settings.Plan.Sampling.Temperature != 0.2 {
		t.Fatalf("expected nested temperature to win, got %#v", settings.Plan.Sampling)
	}
	if settings.Plan.Sampling.TopP == nil || *settings.Plan.Sampling.TopP != 0.9 {
		t.Fatalf("expected nested topP, got %#v", settings.Plan.Sampling)
	}
}

func TestResolveCoderPlanningSettingsUsesPlanningNotPlan(t *testing.T) {
	settings := ResolveCoderPlanningSettings(map[string]any{
		"plan": map[string]any{
			"modelConfig": map[string]any{"modelKey": "plan-tasks-model"},
		},
		"planning": map[string]any{
			"modelConfig": map[string]any{"modelKey": "planning-model"},
		},
		"execute": map[string]any{
			"modelConfig": map[string]any{"modelKey": "coder-execute-model"},
		},
	}, 77)

	if settings.MaxSteps != 77 {
		t.Fatalf("max steps = %d, want 77", settings.MaxSteps)
	}
	if settings.Planning.ModelKey != "planning-model" {
		t.Fatalf("planning model key = %q, want planning-model", settings.Planning.ModelKey)
	}
	if settings.Execute.ModelKey != "coder-execute-model" {
		t.Fatalf("execute model key = %q, want coder-execute-model", settings.Execute.ModelKey)
	}

	legacyOnly := ResolveCoderPlanningSettings(map[string]any{
		"plan": map[string]any{
			"modelConfig": map[string]any{"modelKey": "plan-tasks-model"},
		},
	}, 60)
	if !legacyOnly.Planning.IsZero() || !legacyOnly.Execute.IsZero() {
		t.Fatalf("legacy plan stage must not configure CODER planning: %#v", legacyOnly)
	}
}
