package query

import (
	"fmt"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/interaction"
	runtimetypes "agent-platform/internal/runtime/types"
)

// defaultLevel is the target Agent's effective default: a disabled selector
// still accepts an explicit value that changes nothing.
func ValidateInteractionInput(config interaction.Config, req runtimetypes.QueryCommand, defaultLevel string) error {
	requested, _ := contracts.NormalizeRequestedAccessLevel(req.AccessLevel)
	if !config.AccessLevel && !contracts.IsDefaultAccessLevelRequest(req.AccessLevel) && requested != defaultLevel {
		return queryReferenceStatusError(400, "interaction_disabled", "interactionConfig.accessLevel is disabled")
	}
	model := req.Model != nil && (strings.TrimSpace(req.Model.Key) != "" || strings.TrimSpace(req.Model.ReasoningEffort) != "" || strings.TrimSpace(req.Model.ServiceTier) != "")
	err := config.Validate(model, len(queryinput.ResourceIDs(req.References, "skill")) > 0)
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
