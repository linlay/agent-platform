package kbase

import agentcontract "agent-platform/internal/agent"

func MainSystemInitSpec() agentcontract.SystemInitSpec {
	return agentcontract.SystemInitSpec{
		CacheKey:              MainCacheKey,
		FingerprintStage:      MainStage,
		PromptStage:           MainStage,
		Mode:                  MainStage,
		Stage:                 "main",
		UseSharedSystemPrompt: true,
		IncludeAfterCallHints: true,
		Initial:               true,
	}
}

func EditingSystemInitSpec() agentcontract.SystemInitSpec {
	return agentcontract.SystemInitSpec{
		CacheKey:              EditingCacheKey,
		FingerprintStage:      EditingStage,
		PromptStage:           EditingStage,
		Mode:                  MainStage,
		Stage:                 "editing",
		ToolNames:             EditingToolNames(),
		UseSharedSystemPrompt: true,
		IncludeAfterCallHints: true,
		Initial:               true,
	}
}
