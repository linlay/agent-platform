package contracts

import "testing"

func TestStageSettingsParsesMaxOutputTokens(t *testing.T) {
	settings := parseStageSettings(map[string]any{
		"maxOutputTokens": 8192,
	})

	if settings.MaxOutputTokens != 8192 {
		t.Fatalf("expected maxOutputTokens 8192, got %d", settings.MaxOutputTokens)
	}
	if settings.IsZero() {
		t.Fatal("expected stage settings with maxOutputTokens to be non-zero")
	}
}

func TestStageSettingsIgnoresLegacyMaxTokens(t *testing.T) {
	settings := parseStageSettings(map[string]any{
		"maxTokens": 8192,
	})

	if settings.MaxOutputTokens != 0 {
		t.Fatalf("expected legacy maxTokens to be ignored, got %d", settings.MaxOutputTokens)
	}
	if !settings.IsZero() {
		t.Fatal("expected stage settings with only legacy maxTokens to be zero")
	}
}

func TestStageSettingsParsesSamplingWithZeroTemperature(t *testing.T) {
	settings := parseStageSettings(map[string]any{
		"sampling": map[string]any{
			"temperature":      0,
			"top_p":            0.95,
			"presencePenalty":  0,
			"frequencyPenalty": 0.25,
			"seed":             42,
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

func TestResolvePlanExecuteSettingsMergesRootSamplingIntoStages(t *testing.T) {
	settings := ResolvePlanExecuteSettings(map[string]any{
		"sampling": map[string]any{
			"temperature": 0.7,
			"topP":        0.9,
		},
		"plan": map[string]any{
			"sampling": map[string]any{
				"temperature": 0.2,
			},
		},
	}, 0, 0)

	if settings.Plan.Sampling.Temperature == nil || *settings.Plan.Sampling.Temperature != 0.2 {
		t.Fatalf("expected plan temperature override, got %#v", settings.Plan.Sampling)
	}
	if settings.Plan.Sampling.TopP == nil || *settings.Plan.Sampling.TopP != 0.9 {
		t.Fatalf("expected plan topP inherited from root, got %#v", settings.Plan.Sampling)
	}
	if settings.Execute.Sampling.Temperature == nil || *settings.Execute.Sampling.Temperature != 0.7 {
		t.Fatalf("expected execute temperature from root, got %#v", settings.Execute.Sampling)
	}
	if settings.Summary.Sampling.TopP == nil || *settings.Summary.Sampling.TopP != 0.9 {
		t.Fatalf("expected summary topP inherited from execute, got %#v", settings.Summary.Sampling)
	}
}
