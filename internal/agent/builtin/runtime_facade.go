package builtin

import (
	"agent-platform/internal/agent/coder"
	"agent-platform/internal/agent/kbase"
	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

// These aliases and functions form the stable application-facing surface for
// built-in mode behavior. Transport adapters depend on this dispatch package,
// never on a concrete CODER/KBASE/TEAM implementation.
const (
	CoderExecuteCacheKey                  = coder.ExecuteCacheKey
	CoderMainStage                        = coder.MainStage
	CoderPlanningApproveContinuationParam = coder.PlanningApproveContinuationParam
	TeamMode                              = agentteam.Mode
	TeamToolDelegate                      = agentteam.ToolDelegate
)

type CoderContinuationRequestInput = coder.ContinuationRequestInput
type CoderCreateDefaults = coder.CreateDefaults
type KBaseCreateDefaults = kbase.CreateDefaults
type TeamMemberSpec = agentteam.MemberSpec
type TeamPromptConfig = agentteam.PromptConfig
type CoderACPBridgeConfig = coder.ACPBridgeConfig
type CoderACPRoutingConfig = coder.ACPRoutingConfig
type CoderProjectPromptFile = coder.ProjectPromptFile
type CoderWorkspacePromptPolicy = coder.WorkspacePromptPolicy
type CoderWorkspaceGitPolicy = coder.WorkspaceGitPolicy

func IsCoderMode(mode string) bool { return coder.IsMode(mode) }
func IsKBaseMode(mode string) bool { return kbase.IsMode(mode) }
func IsCoderACPBackend(mode, acpBridgeID string) bool {
	return coder.IsACPBackend(mode, acpBridgeID)
}
func IsCoderNativeBackend(mode, acpBridgeID string) bool {
	return coder.IsNativeBackend(mode, acpBridgeID)
}
func CoderPlanningModeEnabled(mode string, requested bool) bool {
	return coder.PlanningModeEnabled(mode, requested)
}
func KBaseEditingModeEnabled(mode string, requested bool) bool {
	return kbase.EditingModeEnabled(mode, requested)
}
func CoderPlanningContinuationDecision(mode string, answer map[string]any) string {
	return coder.PlanningContinuationDecision(mode, answer)
}
func CoderBuildContinuationRequest(input CoderContinuationRequestInput) api.QueryRequest {
	return coder.BuildContinuationRequest(input)
}
func CoderBuildPlanningApproveContinuationRequest(input CoderContinuationRequestInput) api.QueryRequest {
	return coder.BuildPlanningApproveContinuationRequest(input)
}
func CoderPlanningExecuteToolsForStage(stage contracts.StageSettings, toolNames []string) []string {
	return coder.PlanningExecuteToolsForStage(stage, toolNames)
}
func CoderExecuteSyntheticQueryMessage(locale string) string {
	return coder.ExecuteSyntheticQueryMessage(locale)
}
func CoderSubmitPlanningDecision(params api.SubmitParams) string {
	return coder.SubmitPlanningDecision(params)
}
func CoderStartsNewExecutionRun(mode string, answer map[string]any, agentMode, acpBridgeID string) bool {
	return coder.StartsNewExecutionRun(mode, answer, agentMode, acpBridgeID)
}
func CoderModelConfigFromOptions(options api.CoderModelOptionsResponse) map[string]any {
	return coder.ModelConfigFromOptions(options)
}
func CoderModelOptionsFilterMode(agentKey, mode, acpBridgeID string) string {
	return coder.ModelOptionsFilterMode(agentKey, mode, acpBridgeID)
}
func CoderDefaultModelOptionKey(options []api.CoderModelOption, preferredKey, defaultKey string) string {
	return coder.DefaultModelOptionKey(options, preferredKey, defaultKey)
}
func CoderDefaultServiceTier(acp bool, configured string, options []api.ServiceTierOption) string {
	return coder.DefaultServiceTier(acp, configured, options)
}
func CoderServiceTierOptions(acp bool, options []api.CoderModelOption) []api.ServiceTierOption {
	return coder.ServiceTierOptions(acp, options)
}
func CoderReasoningEffortOptions(acp bool, options []api.CoderModelOption) []api.ReasoningEffortOption {
	return coder.ReasoningEffortOptions(acp, options)
}
func CoderNormalizeReasoningEffort(value string) (string, bool) {
	return coder.NormalizeReasoningEffort(value)
}
func CoderNormalizeServiceTier(value string) (string, bool) {
	return coder.NormalizeServiceTier(value)
}
func CoderModelKeyInOptions(key string, options []api.CoderModelOption) bool {
	return coder.ModelKeyInOptions(key, options)
}
func CoderServiceTierAllowedForACPModel(value, key string, options []api.CoderModelOption) bool {
	return coder.ServiceTierAllowedForACPModel(value, key, options)
}
func CoderReasoningEffortAllowedForACPModel(value, key string, options []api.CoderModelOption) bool {
	return coder.ReasoningEffortAllowedForACPModel(value, key, options)
}
func ApplyCoderCreateDefaults(definition map[string]any, defaults CoderCreateDefaults) map[string]any {
	return coder.ApplyCreateDefaults(definition, defaults)
}
func ApplyKBaseCreateDefaults(definition map[string]any, defaults KBaseCreateDefaults) map[string]any {
	return kbase.ApplyCreateDefaults(definition, defaults)
}
func CoderResolveACPBridge(bridgeID string, lookup func(string) (CoderACPBridgeConfig, bool)) (CoderACPRoutingConfig, error) {
	return coder.ResolveACPBridge(bridgeID, lookup)
}
func CoderResolveACPModelOptions(mode, modelKey string, existing *api.QueryModelOptions, resolve func(string) string) *api.QueryModelOptions {
	return coder.ResolveACPModelOptions(mode, modelKey, existing, resolve)
}
func CoderValidateWorkspaceGit(policy CoderWorkspaceGitPolicy) error {
	return coder.ValidateWorkspaceGit(policy)
}
func CoderLoadWorkspacePrompt(policy CoderWorkspacePromptPolicy) (string, error) {
	return coder.LoadWorkspacePrompt(policy)
}
func CoderRuntimeToolNamesForAgent(mode, acpBridgeID, stage string, names []string) []string {
	return coder.RuntimeToolNamesForAgent(mode, acpBridgeID, stage, names)
}
func KBaseDefaultToolNames() []string        { return kbase.DefaultToolNames() }
func TeamDefaultBudget() map[string]any      { return agentteam.DefaultBudget() }
func TeamDefaultToolNames() []string         { return agentteam.DefaultToolNames() }
func TeamDefaultContextTags() []string       { return agentteam.DefaultContextTags() }
func TeamNormalizeMaxParallel(value int) int { return agentteam.NormalizeMaxParallel(value) }
func TeamBuildToolDefinition(base api.ToolDetailResponse, members []TeamMemberSpec) (api.ToolDetailResponse, error) {
	return agentteam.BuildToolDefinition(base, members)
}
func TeamBuildSystemPrompt(config TeamPromptConfig) string {
	return agentteam.BuildSystemPrompt(config)
}
