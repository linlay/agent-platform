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

func TestResolvePlanningModeSettingsSummaryFallsBackToExecute(t *testing.T) {
	settings := ResolvePlanningModeSettings(map[string]any{
		"execute": map[string]any{
			"modelConfig": map[string]any{"maxOutputTokens": 4096},
		},
	}, 0)
	if settings.Summary.MaxOutputTokens != 4096 {
		t.Fatalf("summary without its own stage must reuse execute, got %#v", settings.Summary)
	}

	settings = ResolvePlanningModeSettings(map[string]any{
		"execute": map[string]any{
			"modelConfig": map[string]any{"maxOutputTokens": 4096},
		},
		"summary": map[string]any{
			"modelConfig": map[string]any{
				"sampling": map[string]any{"temperature": 0.2},
			},
		},
	}, 0)
	if settings.Summary.MaxOutputTokens != 0 || settings.Summary.Sampling.Temperature == nil || *settings.Summary.Sampling.Temperature != 0.2 {
		t.Fatalf("explicit summary stage must replace execute, got %#v", settings.Summary)
	}
}

func TestResolvePlanningModeSettingsUsesPlanningNotPlan(t *testing.T) {
	settings := ResolvePlanningModeSettings(map[string]any{
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

	legacyOnly := ResolvePlanningModeSettings(map[string]any{
		"plan": map[string]any{
			"modelConfig": map[string]any{"modelKey": "plan-tasks-model"},
		},
	}, 60)
	if !legacyOnly.Planning.IsZero() || !legacyOnly.Execute.IsZero() {
		t.Fatalf("legacy plan stage must not configure CODER planning: %#v", legacyOnly)
	}
}
