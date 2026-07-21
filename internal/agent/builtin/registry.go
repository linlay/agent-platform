// Package builtin is the single static dispatch point for built-in agent
// modes. It is intentionally a small registry, not a plugin lifecycle.
package builtin

import (
	"strings"

	agentcontract "agent-platform/internal/agent"
	"agent-platform/internal/agent/coder"
	"agent-platform/internal/agent/kbase"
	agentteam "agent-platform/internal/agent/team"
	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func Lookup(mode string) (agentcontract.ModeDescriptor, bool) {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case coder.Mode:
		return coder.Descriptor().Clone(), true
	case kbase.Mode:
		return kbase.Descriptor().Clone(), true
	case agentteam.Mode:
		return agentteam.Descriptor().Clone(), true
	default:
		return agentcontract.ModeDescriptor{}, false
	}
}

// ProfileForAgent resolves the built-in profile for an AgentDefinition. CODER
// has backend-specific platform tools: ACP delegates execution to a bridge,
// while native CODER owns the local tool loop.
func ProfileForAgent(mode string, acpBridgeID string) (agentcontract.ModeProfile, bool) {
	descriptor, ok := Lookup(mode)
	if !ok {
		return agentcontract.ModeProfile{}, false
	}
	if strings.EqualFold(strings.TrimSpace(mode), coder.Mode) {
		descriptor.Profile.ToolNames = coder.DefaultToolNamesForBackend(acpBridgeID)
	}
	return descriptor.Profile, true
}

func MainSystemInitSpec(mode string) (agentcontract.SystemInitSpec, bool) {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case coder.Mode:
		return coder.MainSystemInitSpec(), true
	case kbase.Mode:
		return kbase.MainSystemInitSpec(), true
	case agentteam.Mode:
		return agentteam.MainSystemInitSpec(), true
	default:
		return agentcontract.SystemInitSpec{}, false
	}
}

func ConfiguredSystemPrompt(mode string, coderPrompt string, kbasePrompt string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case coder.Mode:
		return strings.TrimSpace(coderPrompt)
	case kbase.Mode:
		return strings.TrimSpace(kbasePrompt)
	case agentteam.Mode:
		return agentteam.DefaultSystemPrompt
	default:
		return ""
	}
}

func RenderSystemPrompt(session contracts.QuerySession, req api.QueryRequest, toolNames []string, stage string) string {
	switch strings.ToUpper(strings.TrimSpace(session.Mode)) {
	case coder.Mode:
		return coder.RenderSystemPrompt(session, req, toolNames, stage)
	case kbase.Mode:
		return kbase.RenderSystemPrompt(session, req, toolNames, stage)
	case agentteam.Mode:
		return agentteam.RenderSystemPrompt(session, req, toolNames, stage)
	default:
		return ""
	}
}
