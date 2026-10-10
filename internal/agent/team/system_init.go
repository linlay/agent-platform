package team

import agentcontract "agent-platform/internal/agent"

func MainSystemInitSpec() agentcontract.SystemInitSpec {
	return agentcontract.SystemInitSpec{
		CacheKey:              MainCacheKey,
		FingerprintStage:      MainStage,
		PromptStage:           MainStage,
		Mode:                  MainStage,
		Stage:                 "main",
		ToolNames:             nil,
		UseSharedSystemPrompt: true,
		IncludeAfterCallHints: true,
		Initial:               true,
	}
}
