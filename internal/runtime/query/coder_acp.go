package query

import (
	"strings"

	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
)

func (s *Service) acpCoderModelOptions(session contracts.QuerySession, existing *queryinput.QueryModelOptions) *queryinput.QueryModelOptions {
	return agentbuiltin.CoderResolveACPModelOptions(session.Mode, session.ModelKey, existing, func(modelKey string) string {
		if s != nil && s.deps.Models != nil {
			if model, err := s.deps.Models.GetModel(modelKey); err == nil && strings.TrimSpace(model.ModelID) != "" {
				return strings.TrimSpace(model.ModelID)
			}
		}
		return ""
	})
}
