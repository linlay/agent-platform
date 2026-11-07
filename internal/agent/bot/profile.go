// Package bot owns the scheduling Agent mode. A BOT has one dispatch Chat
// and execution workers; unlike ordinary modes it carries runtime policy.
package bot

import agentcontract "agent-platform/internal/agent"

const Mode = "BOT"

func Descriptor() agentcontract.ModeDescriptor {
	return agentcontract.ModeDescriptor{Mode: Mode, MainStage: "bot", MainCacheKey: "bot:main", CreatePrefix: "bot", Profile: agentcontract.ModeProfile{IconName: "bot"}, Capabilities: agentcontract.ModeCapabilities{InvokeChildren: true}}
}

func MainSystemInitSpec() agentcontract.SystemInitSpec {
	return agentcontract.SystemInitSpec{CacheKey: "bot:main", FingerprintStage: "bot", PromptStage: "bot", Mode: "bot", Stage: "main", UseSharedSystemPrompt: true, IncludeAfterCallHints: true, Initial: true}
}
