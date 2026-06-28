package llm

import "agent-platform/internal/hitl"

func normalizeHITLSubmit(args map[string]any, params any) (map[string]any, error) {
	return hitl.Normalize(args, params)
}

func normalizeHITLPlanSubmit(args map[string]any, params any) (map[string]any, error) {
	return hitl.NormalizePlan(args, params)
}
