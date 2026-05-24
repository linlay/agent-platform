package llm

import "agent-platform/internal/hitl"

func normalizeHITLSubmit(args map[string]any, params any) (map[string]any, error) {
	return hitl.Normalize(args, params)
}

func normalizeHITLApprovalSubmit(args map[string]any, params any) (map[string]any, error) {
	return hitl.NormalizeApproval(args, params)
}
