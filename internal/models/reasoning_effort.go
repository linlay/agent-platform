package models

import (
	"fmt"
	"strings"
)

const (
	ReasoningEffortNone   = "NONE"
	ReasoningEffortLow    = "LOW"
	ReasoningEffortMedium = "MEDIUM"
	ReasoningEffortHigh   = "HIGH"
	ReasoningEffortXHigh  = "XHIGH"
	ReasoningEffortMax    = "MAX"
)

var reasoningEffortOrder = []string{
	ReasoningEffortNone,
	ReasoningEffortLow,
	ReasoningEffortMedium,
	ReasoningEffortHigh,
	ReasoningEffortXHigh,
	ReasoningEffortMax,
}

var activeReasoningEffortOrder = []string{
	ReasoningEffortLow,
	ReasoningEffortMedium,
	ReasoningEffortHigh,
	ReasoningEffortXHigh,
	ReasoningEffortMax,
}

func ReasoningEfforts() []string {
	return append([]string(nil), reasoningEffortOrder...)
}

func ActiveReasoningEfforts() []string {
	return append([]string(nil), activeReasoningEffortOrder...)
}

func NormalizeReasoningEffort(value string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "":
		return "", true
	case ReasoningEffortNone:
		return ReasoningEffortNone, true
	case ReasoningEffortLow:
		return ReasoningEffortLow, true
	case ReasoningEffortMedium:
		return ReasoningEffortMedium, true
	case ReasoningEffortHigh:
		return ReasoningEffortHigh, true
	case ReasoningEffortXHigh, "EXTRA_HIGH":
		return ReasoningEffortXHigh, true
	case ReasoningEffortMax:
		return ReasoningEffortMax, true
	default:
		return "", false
	}
}

func ParseReasoningEffortMapping(raw any, modelType string, protocol string) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	node, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("reasoningEffortMapping must be a map")
	}
	if modelType != ModelTypeChat {
		return nil, fmt.Errorf("reasoningEffortMapping is only supported for type: chat")
	}
	normalizedProtocol := strings.ToUpper(strings.TrimSpace(protocol))
	if normalizedProtocol == "" {
		normalizedProtocol = "OPENAI"
	}
	if normalizedProtocol != "OPENAI" {
		return nil, fmt.Errorf("reasoningEffortMapping is only supported for native OPENAI chat models")
	}

	result := make(map[string]string, len(node))
	active := make(map[string]struct{}, len(activeReasoningEffortOrder))
	for _, effort := range activeReasoningEffortOrder {
		active[effort] = struct{}{}
	}
	for key, rawValue := range node {
		if _, allowed := active[key]; !allowed {
			return nil, fmt.Errorf("reasoningEffortMapping key %q must be one of LOW, MEDIUM, HIGH, XHIGH, MAX", key)
		}
		value, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("reasoningEffortMapping.%s must be a string", key)
		}
		value = strings.TrimSpace(value)
		if _, allowed := active[value]; !allowed {
			return nil, fmt.Errorf("reasoningEffortMapping.%s must be one of LOW, MEDIUM, HIGH, XHIGH, MAX", key)
		}
		result[key] = value
	}
	for _, effort := range activeReasoningEffortOrder {
		if _, exists := result[effort]; !exists {
			return nil, fmt.Errorf("reasoningEffortMapping must define %s", effort)
		}
	}
	return result, nil
}

func ResolveReasoningEffort(mapping map[string]string, logical string) (string, bool) {
	if len(mapping) == 0 {
		return "", false
	}
	logical, ok := NormalizeReasoningEffort(logical)
	if !ok || logical == "" || logical == ReasoningEffortNone {
		return "", false
	}
	actual, ok := mapping[logical]
	return actual, ok
}
