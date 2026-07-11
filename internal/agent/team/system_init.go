package team

import agentcontract "agent-platform/internal/agent"

func MainSystemInitSpec() agentcontract.SystemInitSpec {
	return agentcontract.SystemInitSpec{
		CacheKey:              MainCacheKey,
		FingerprintStage:      MainStage,
		PromptStage:           MainStage,
		Mode:                  MainStage,
		Stage:                 "main",
		ToolNames:             DefaultToolNames(),
		UseSharedSystemPrompt: true,
		IncludeAfterCallHints: false,
		Initial:               true,
	}
}
