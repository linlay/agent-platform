package catalog

import (
	agentcontract "agent-platform/internal/agent"
	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/contracts"
	"fmt"
	"strings"
)

func ResolvedModeCapabilities(def AgentDefinition) agentcontract.ModeCapabilities {
	if descriptor, ok := agentbuiltin.Lookup(def.Mode); ok {
		capabilities := descriptor.Capabilities
		if agentbuiltin.IsCoderACPBackend(def.Mode, def.ACPBridgeID) {
			capabilities.RunAsChild = false
		}
		return capabilities
	}
	switch strings.ToUpper(strings.TrimSpace(def.Mode)) {
	case AgentModeGeneral, "REACT", "ONESHOT", AgentModeProxy:
		return agentcontract.ModeCapabilities{InvokeChildren: true, RunAsChild: true}
	default:
		return agentcontract.ModeCapabilities{}
	}
}

// AgentInvocationError describes static agent_invoke target eligibility. Caller
// authorization, self-target checks and live availability remain runtime checks.
func AgentInvocationError(def AgentDefinition) error {
	if !AgentUsesACPCoderBackend(def) && !ResolvedModeCapabilities(def).RunAsChild {
		return fmt.Errorf("sub-agent must be GENERAL/ONESHOT/CODER/KBASE/PROXY")
	}
	if !AgentInvocable(def) {
		return fmt.Errorf("sub-agent is not invocable")
	}
	for _, tool := range def.Tools {
		if strings.EqualFold(strings.TrimSpace(tool), contracts.InvokeAgentsToolName) {
			return fmt.Errorf("nested sub-agent invocation is not allowed")
		}
	}
	return nil
}
