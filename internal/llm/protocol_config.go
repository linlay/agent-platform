package llm

import (
	"strings"

	. "agent-platform-runner-go/internal/contracts"
	. "agent-platform-runner-go/internal/models"
)

type protocolRuntimeConfig struct {
	EndpointPath string
	Headers      map[string]string
	Compat       map[string]any
}

func resolveProtocolRuntimeConfig(provider ProviderDefinition, model ModelDefinition) protocolRuntimeConfig {
	protocol := strings.ToUpper(strings.TrimSpace(model.Protocol))
	if protocol == "" {
		protocol = "OPENAI"
	}
	def := provider.Protocol(protocol)
	endpointPath := def.EndpointPath
	if endpointPath == "" {
		endpointPath = defaultEndpointPath(protocol, provider.BaseURL)
	}
	return protocolRuntimeConfig{
		EndpointPath: endpointPath,
		Headers:      mergeStringMaps(defaultProtocolHeaders(protocol), def.Headers, model.Headers),
		Compat:       mergeAnyMaps(mergeAnyMaps(defaultProtocolCompat(protocol), def.Compat), model.Compat),
	}
}

func defaultProtocolHeaders(protocol string) map[string]string {
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "ANTHROPIC":
		return map[string]string{
			"anthropic-version": "2023-06-01",
		}
	default:
		return nil
	}
}

func defaultProtocolCompat(protocol string) map[string]any {
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "ANTHROPIC":
		return map[string]any{
			"request": map[string]any{
				"whenReasoningEnabled": map[string]any{
					"thinking": map[string]any{},
				},
			},
			"response": map[string]any{
				"reasoningFormat": "ANTHROPIC_THINKING_DELTA",
			},
		}
	default:
		return map[string]any{
			"request": map[string]any{
				"whenReasoningEnabled": map[string]any{},
			},
			"response": map[string]any{
				"reasoningFormat": "REASONING_CONTENT",
			},
		}
	}
}

func mergeStringMaps(maps ...map[string]string) map[string]string {
	var out map[string]string
	for _, current := range maps {
		if len(current) == 0 {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		for key, value := range current {
			out[key] = value
		}
	}
	return out
}

func mergeAnyMaps(base map[string]any, overlay map[string]any) map[string]any {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	out := CloneMap(base)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range overlay {
		if baseValue, ok := out[key].(map[string]any); ok {
			if overlayValue, ok := value.(map[string]any); ok {
				out[key] = mergeAnyMaps(baseValue, overlayValue)
				continue
			}
		}
		out[key] = value
	}
	return out
}
