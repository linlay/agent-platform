package modelrequest

import "agent-platform/internal/contracts"

const DeterministicTemperature = 0.0

func ApplyDeterministicTemperature(body map[string]any) {
	if body == nil {
		return
	}
	body["temperature"] = DeterministicTemperature
}

func ApplyOpenAICompatibleSampling(body map[string]any, sampling contracts.SamplingSettings) {
	if body == nil || sampling.IsZero() {
		return
	}
	if sampling.Temperature != nil {
		body["temperature"] = *sampling.Temperature
	}
	if sampling.TopP != nil {
		body["top_p"] = *sampling.TopP
	}
	if sampling.PresencePenalty != nil {
		body["presence_penalty"] = *sampling.PresencePenalty
	}
	if sampling.FrequencyPenalty != nil {
		body["frequency_penalty"] = *sampling.FrequencyPenalty
	}
	if sampling.Seed != nil {
		body["seed"] = *sampling.Seed
	}
}

func ApplyAnthropicSampling(body map[string]any, sampling contracts.SamplingSettings) {
	if body == nil || sampling.IsZero() {
		return
	}
	if sampling.Temperature != nil {
		body["temperature"] = *sampling.Temperature
	}
	if sampling.TopP != nil {
		body["top_p"] = *sampling.TopP
	}
}
