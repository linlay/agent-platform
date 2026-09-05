package builtin

import agentcontract "agent-platform/internal/agent"

func MainSystemInitSpec(mode string) (agentcontract.SystemInitSpec, bool) {
	return SystemInitSpec(mode, false)
}
