package kbase

import corekbase "agent-platform/internal/kbase"

type CreateDefaults = corekbase.CreateDefaults

func ApplyCreateDefaults(definition map[string]any, defaults CreateDefaults) map[string]any {
	return corekbase.ApplyCreateDefaults(definition, defaults)
}
