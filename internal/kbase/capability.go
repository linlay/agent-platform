package kbase

type Requirement string

const (
	RequirementOptional Requirement = "optional"
	RequirementRequired Requirement = "required"
)

// AgentSpec is the fully resolved, mode-neutral KBASE capability owned by one
// catalog agent. The KBASE runtime deliberately has no dependency on agent
// mode; the app adapter resolves mode policy before producing this value.
type AgentSpec struct {
	Key           string
	Requirement   Requirement
	WorkspaceRoot string
	Config        Config
}
