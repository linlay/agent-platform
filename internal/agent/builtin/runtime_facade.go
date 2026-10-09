package builtin

import (
	"strings"

	"agent-platform/internal/agent/coder"
	"agent-platform/internal/agent/general"
	"agent-platform/internal/agent/kbase"
	"agent-platform/internal/agent/planmode"
	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

// These aliases and functions form the stable application-facing surface for
// built-in mode behavior. Transport adapters depend on this dispatch package,
// never on a concrete CODER/KBASE/TEAM implementation.
const (
	CoderMainStage               = coder.MainStage
	PlanApproveContinuationParam = planmode.ApproveContinuationParam
	TeamMode                     = agentteam.Mode
	TeamToolDelegate             = agentteam.ToolDelegate
)

type PlanContinuationRequestInput = planmode.ContinuationRequestInput
type CoderCreateDefaults = coder.CreateDefaults
type KBaseCreateDefaults = kbase.CreateDefaults
type GeneralCreateDefaults = general.CreateDefaults
type GeneralWorkspacePromptPolicy = general.WorkspacePromptPolicy
type TeamMemberSpec = agentteam.MemberSpec
type TeamPromptConfig = agentteam.PromptConfig
type CoderACPBridgeConfig = coder.ACPBridgeConfig
type CoderACPRoutingConfig = coder.ACPRoutingConfig
type CoderProjectPromptFile = coder.ProjectPromptFile
type CoderWorkspacePromptPolicy = coder.WorkspacePromptPolicy
type CoderWorkspaceGitPolicy = coder.WorkspaceGitPolicy

func IsCoderMode(mode string) bool     { return coder.IsMode(mode) }
func IsKBaseMode(mode string) bool     { return kbase.IsMode(mode) }
func IsGeneralMode(mode string) bool   { return general.IsMode(mode) }
func GeneralCreateToolNames() []string { return general.CreateToolNames() }
func CoderCreateToolNames() []string   { return coder.CreateToolNames() }

const GeneralCreatePrefix = general.CreatePrefix

func IsCoderACPBackend(mode, acpBridgeID string) bool {
	return coder.IsACPBackend(mode, acpBridgeID)
}
func IsCoderNativeBackend(mode, acpBridgeID string) bool {
	return coder.IsNativeBackend(mode, acpBridgeID)
}

// PlanningModeSupported reports whether planningMode is accepted for an Agent
// mode. Planning is a capability of ordinary native Agents, not of one mode;
// TEAM coordinators and pipeline modes do not offer it.
func PlanningModeSupported(mode string) bool {
	return general.IsMode(mode) || coder.IsMode(mode) || kbase.IsMode(mode)
}

// NativePlanning reports whether the platform itself runs the Agent's planning
// Runs. An ACP bridge receives planningMode and owns the flow instead.
func NativePlanning(mode, acpBridgeID string) bool {
	return PlanningModeSupported(mode) && strings.TrimSpace(acpBridgeID) == ""
}
func PlanningModeEnabled(mode string, requested bool) bool {
	return requested && PlanningModeSupported(mode)
}
func KBaseEditingModeEnabled(mode string, requested bool) bool {
	return kbase.EditingModeEnabled(mode, requested)
}
func PlanContinuationDecision(mode string, answer map[string]any) string {
	return planmode.PlanningContinuationDecision(mode, answer)
}
func BuildPlanContinuationRequest(input PlanContinuationRequestInput) api.QueryRequest {
	return planmode.BuildContinuationRequest(input)
}
func BuildConfirmedPlanRequest(input PlanContinuationRequestInput) api.QueryRequest {
	return planmode.BuildConfirmedPlanRequest(input)
}
func IsConfirmedPlanRun(params map[string]any) bool {
	return planmode.IsConfirmedPlanRun(params)
}
func ConfirmedPlanTools(session contracts.QuerySession, defs []api.ToolDetailResponse) []string {
	return planmode.ConfirmedPlanTools(session, defs)
}
func PlanExecuteSyntheticQueryMessage(locale string) string {
	return planmode.ExecuteSyntheticQueryMessage(locale)
}
func SubmitPlanningDecision(param api.SubmitParam) string {
	return planmode.SubmitPlanningDecision(param)
}
func StartsNewExecutionRun(mode string, answer map[string]any, agentMode, acpBridgeID string) bool {
	return planmode.StartsNewExecutionRun(mode, answer, NativePlanning(agentMode, acpBridgeID))
}
func CoderModelOptionsFilterMode(agentKey, mode, acpBridgeID string) string {
	return coder.ModelOptionsFilterMode(agentKey, mode, acpBridgeID)
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
func ApplyGeneralCreateDefaults(definition map[string]any, defaults GeneralCreateDefaults) map[string]any {
	return general.ApplyCreateDefaults(definition, defaults)
}
func GeneralLoadWorkspacePrompt(policy GeneralWorkspacePromptPolicy) (string, error) {
	return general.LoadWorkspacePrompt(policy)
}
func ApplyKBaseCreateDefaults(definition map[string]any, defaults KBaseCreateDefaults) map[string]any {
	return kbase.ApplyCreateDefaults(definition, defaults)
}
func ApplyKBaseCreateToolDefaults(definition map[string]any) map[string]any {
	return kbase.ApplyCreateToolDefaults(definition)
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
func KBaseCreateToolNames() []string         { return kbase.CreateToolNames() }
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
