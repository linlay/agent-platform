package query

import (
	"fmt"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/interaction"
	runtimetypes "agent-platform/internal/runtime/types"
)

func ValidateInteractionInput(config interaction.Config, req runtimetypes.QueryCommand, defaults ...string) error {
	defaultLevel := "default"
	if len(defaults) > 0 {
		defaultLevel = defaults[0]
	}
	requested, _ := contracts.NormalizeAccessLevel(req.AccessLevel)
	if !config.AccessLevel && strings.TrimSpace(req.AccessLevel) != "" && requested != defaultLevel {
		return queryReferenceStatusError(400, "interaction_disabled", "interactionConfig.accessLevel is disabled")
	}
	model := req.Model != nil && (strings.TrimSpace(req.Model.Key) != "" || strings.TrimSpace(req.Model.ReasoningEffort) != "" || strings.TrimSpace(req.Model.ServiceTier) != "")
	err := config.Validate(model, "", len(queryinput.ResourceIDs(req.References, "skill")) > 0)
	if err == nil && len(queryinput.ResourceIDs(req.References, "kbase")) > 0 && !config.Attachment.Kbases {
		err = fmt.Errorf("interactionConfig.attachment.kbases is disabled")
	}
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
