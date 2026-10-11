package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/interaction"
	"agent-platform/internal/runtime/query"
)

var buildBTWUserMessage = query.BuildBTWUserMessage
var applyQueryModelOptionsToSession = query.ApplyQueryModelOptionsToSession

func validateInteractionInput(config interaction.Config, req api.QueryRequest) error {
	return query.ValidateInteractionInput(config, queryCommandFromAPI(req))
}

func validateSubmitParams(ctx contracts.AwaitingSubmitContext, params api.SubmitParams) error {
	return query.ValidateSubmitParams(ctx, api.SubmitRequest{Params: params})
}

func validateSubmitParam(ctx contracts.AwaitingSubmitContext, param api.SubmitParam) error {
	return query.ValidateSubmitParams(ctx, api.SubmitRequest{Param: param})
}

func validateDeferredSubmitParams(mode string, params api.SubmitParams) error {
	return query.ValidateDeferredSubmitParams(mode, api.SubmitRequest{Params: params})
}

func validateDeferredSubmitParam(mode string, param api.SubmitParam) error {
	return query.ValidateDeferredSubmitParams(mode, api.SubmitRequest{Param: param})
}

var configureTeamCoordinatorSession = query.ConfigureTeamCoordinatorSession
var teamDelegateBaseDefinition = query.TeamDelegateBaseDefinition
