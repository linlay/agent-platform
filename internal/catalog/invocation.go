package catalog

import (
	agentcontract "agent-platform/internal/agent"
	agentbuiltin "agent-platform/internal/agent/builtin"
	"agent-platform/internal/contracts"
	"fmt"
	"strings"
)

// ExecutionBackend is independent of native mode and never infers ACP from a bridge ID.
func ExecutionBackend(def AgentDefinition) string {
	if def.Engine == AgentEngineACP {
		return AgentEngineACP
	}
	switch strings.ToUpper(strings.TrimSpace(def.Mode)) {
	case AgentModeProxy:
		return "proxy"
	case AgentModeChannel:
		return "channel"
	default:
		return AgentEngineNative
	}
}

func ResolvedModeCapabilities(def AgentDefinition) agentcontract.ModeCapabilities {
	if ExecutionBackend(def) == AgentEngineACP {
		return agentcontract.ModeCapabilities{RunAsChild: true}
	}
	if descriptor, ok := agentbuiltin.Lookup(def.Mode); ok {
		capabilities := descriptor.Capabilities
		return capabilities
	}
	switch strings.ToUpper(strings.TrimSpace(def.Mode)) {
	case AgentModeGeneral, "REACT", "ONESHOT", AgentModeExecutor, AgentModeProxy:
		return agentcontract.ModeCapabilities{InvokeChildren: true, RunAsChild: true}
	default:
		return agentcontract.ModeCapabilities{}
	}
}

// AgentInvocationError describes static agent_invoke target eligibility. Caller
// authorization, self-target checks and live availability remain runtime checks.
func AgentInvocationError(def AgentDefinition) error {
	if !ResolvedModeCapabilities(def).RunAsChild {
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
