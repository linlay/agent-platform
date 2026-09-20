package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/interaction"
	"net/http"
	"strings"
)

func validateInteractionInput(config interaction.Config, req api.QueryRequest) error {
	model := req.Model != nil && (strings.TrimSpace(req.Model.Key) != "" || strings.TrimSpace(req.Model.ReasoningEffort) != "" || strings.TrimSpace(req.Model.ServiceTier) != "")
	err := config.Validate(model, strings.TrimSpace(req.AccessLevel), len(normalizeMustUseSkills(req.MustUseSkills)) > 0)
	if err == nil {
		for _, ref := range req.References {
			if err = config.ValidateReference(ref.Type); err != nil {
				break
			}
		}
	}
	if err != nil {
		return queryReferenceStatusError(http.StatusBadRequest, "interaction_disabled", err.Error())
	}
	return nil
}
