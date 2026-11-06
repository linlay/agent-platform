package builtin

import (
	"strings"

	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/agent/coder"
	"agent-platform/internal/agent/executor"
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
	PlanApproveContinuationParam = planmode.ApproveContinuationParam
	TeamToolDelegate             = agentteam.ToolDelegate
)

type PlanContinuationRequestInput = planmode.ContinuationRequestInput
type CoderCreateDefaults = coder.CreateDefaults
type KBaseCreateDefaults = kbase.CreateDefaults
type GeneralCreateDefaults = general.CreateDefaults
type TeamMemberSpec = agentteam.MemberSpec
type TeamPromptConfig = agentteam.PromptConfig

type ProjectPromptFile = agentcontract.ProjectPromptFile
type WorkspacePromptPolicy = agentcontract.WorkspacePromptPolicy
type WorkspaceGitPolicy = agentcontract.WorkspaceGitPolicy

func IsKBaseMode(mode string) bool     { return kbase.IsMode(mode) }
func IsGeneralMode(mode string) bool   { return general.IsMode(mode) }
func IsExecutorMode(mode string) bool  { return executor.IsMode(mode) }
func GeneralCreateToolNames() []string { return general.CreateToolNames() }
func CoderCreateToolNames() []string   { return coder.CreateToolNames() }

const GeneralCreatePrefix = general.CreatePrefix
const ExecutorCreatePrefix = executor.CreatePrefix

// PlanningModeSupported reports whether planningMode is accepted for an Agent
// mode. Planning is a capability of ordinary native Agents, not of one mode;
// TEAM coordinators and pipeline modes do not offer it.
func PlanningModeSupported(mode string) bool {
	return general.IsMode(mode) || coder.IsMode(mode) || kbase.IsMode(mode) || executor.IsMode(mode) || strings.EqualFold(mode, "TEAM")
}

func PlanningModeEnabled(mode string, requested bool) bool {
	return requested && PlanningModeSupported(mode)
}

// WorkspaceEditingDefault reads a template value, never a mode-specific grant.
func WorkspaceEditingDefault(mode string) bool {
	if executor.IsMode(mode) {
		return !executor.WorkspaceReadOnly
	}
	descriptor, ok := Lookup(mode)
	return !ok || !descriptor.Profile.WorkspaceReadOnly
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
func ExecuteSyntheticQueryMessage(locale string) string {
	return planmode.ExecuteSyntheticQueryMessage(locale)
}
func SubmitPlanningDecision(param api.SubmitParam) string {
	return planmode.SubmitPlanningDecision(param)
}
func StartsNewExecutionRun(mode string, answer map[string]any, agentMode string) bool {
	return planmode.StartsNewExecutionRun(mode, answer, PlanningModeSupported(agentMode))
}

func CoderNormalizeReasoningEffort(value string) (string, bool) {
	return coder.NormalizeReasoningEffort(value)
}
func CoderNormalizeServiceTier(value string) (string, bool) {
	return coder.NormalizeServiceTier(value)
}

func ApplyCoderCreateDefaults(definition map[string]any, defaults CoderCreateDefaults) map[string]any {
	return coder.ApplyCreateDefaults(definition, defaults)
}
func ApplyGeneralCreateDefaults(definition map[string]any, defaults GeneralCreateDefaults) map[string]any {
	return general.ApplyCreateDefaults(definition, defaults)
}
func ApplyKBaseCreateDefaults(definition map[string]any, defaults KBaseCreateDefaults) map[string]any {
	return kbase.ApplyCreateDefaults(definition, defaults)
}
func ApplyKBaseCreateToolDefaults(definition map[string]any) map[string]any {
	return kbase.ApplyCreateToolDefaults(definition)
}

func ValidateWorkspaceGit(policy WorkspaceGitPolicy) error {
	return agentcontract.ValidateWorkspaceGit(policy)
}
func LoadWorkspacePrompt(policy WorkspacePromptPolicy) (string, error) {
	return agentcontract.LoadWorkspacePrompt(policy)
}
func KBaseCreateToolNames() []string         { return kbase.CreateToolNames() }
func TeamNormalizeMaxParallel(value int) int { return agentteam.NormalizeMaxParallel(value) }
func TeamBuildToolDefinition(base api.ToolDetailResponse, members []TeamMemberSpec) (api.ToolDetailResponse, error) {
	return agentteam.BuildToolDefinition(base, members)
}
func TeamBuildSystemPrompt(config TeamPromptConfig) string {
	return agentteam.BuildSystemPrompt(config)
}
