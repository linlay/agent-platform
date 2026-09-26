package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/interaction"
	"agent-platform/internal/runtime/query"
)

var buildBTWUserMessage = query.BuildBTWUserMessage
var applyQueryModelOptionsToSession = query.ApplyQueryModelOptionsToSession

func validateInteractionInput(config interaction.Config, req api.QueryRequest) error {
	return query.ValidateInteractionInput(config, queryCommandFromAPI(req))
}

var validateSubmitParams = query.ValidateSubmitParams
var validateDeferredSubmitParams = query.ValidateDeferredSubmitParams
var hiddenTeamAgentKey = query.HiddenTeamAgentKey
var configureTeamCoordinatorSession = query.ConfigureTeamCoordinatorSession
var teamDelegateBaseDefinition = query.TeamDelegateBaseDefinition
