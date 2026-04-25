package llm

import "agent-platform-runner-go/internal/hitlsubmit"

func normalizeHITLSubmit(args map[string]any, params any) (map[string]any, error) {
	return hitlsubmit.Normalize(args, params)
}

func normalizeHITLApprovalSubmit(args map[string]any, params any) (map[string]any, error) {
	return hitlsubmit.NormalizeApproval(args, params)
}

func normalizeHITLFormSubmit(args map[string]any, params any) (map[string]any, error) {
	return hitlsubmit.NormalizeForm(args, params)
}
