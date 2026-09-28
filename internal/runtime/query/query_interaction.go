package query

import (
	"strings"

	"agent-platform/internal/interaction"
	sessionbuild "agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
)

func ValidateInteractionInput(config interaction.Config, req runtimetypes.QueryCommand) error {
	model := req.Model != nil && (strings.TrimSpace(req.Model.Key) != "" || strings.TrimSpace(req.Model.ReasoningEffort) != "" || strings.TrimSpace(req.Model.ServiceTier) != "")
	err := config.Validate(model, strings.TrimSpace(req.AccessLevel), len(sessionbuild.NormalizeMustUseSkills(req.MustUseSkills)) > 0)
	if err == nil {
		for _, ref := range req.References {
			if err = config.ValidateReference(ref.Type); err != nil {
				break
			}
		}
	}
	if err != nil {
		return queryReferenceStatusError(400, "interaction_disabled", err.Error())
	}
	return nil
}
