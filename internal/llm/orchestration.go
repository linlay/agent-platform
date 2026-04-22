package llm

import "agent-platform-runner-go/internal/contracts"

type OrchestratableAgentStream interface {
	contracts.AgentStream
	InjectToolResult(toolID string, text string, isError bool) bool
	FinalAssistantContent() (string, bool)
}
