package hitl

import "agent-platform-runner-go/internal/api"

type Checker interface {
	Check(command string, chatLevel int) InterceptResult
	Tool(name string) (api.ToolDetailResponse, bool)
	Tools() []api.ToolDetailResponse
}
