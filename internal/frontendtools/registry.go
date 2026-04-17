package frontendtools

import (
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/stream"
)

type Handler interface {
	ToolName() string
	ValidateArgs(args map[string]any) error
	BuildInitialAwaitAsk(toolID string, runID string, tool api.ToolDetailResponse, chunkIndex int, timeoutMs int64) *stream.AwaitAsk
	BuildDeferredAwait(toolID string, runID string, tool api.ToolDetailResponse, args map[string]any, timeoutMs int64) []contracts.AgentDelta
	NormalizeSubmit(args map[string]any, params any) (map[string]any, error)
	FormatSubmitResult(format string, result contracts.ToolExecutionResult) (string, bool)
}

type Registry struct {
	handlers map[string]Handler
}

func NewRegistry(handlers ...Handler) *Registry {
	byName := make(map[string]Handler, len(handlers))
	for _, handler := range handlers {
		if handler == nil {
			continue
		}
		name := normalizeToolName(handler.ToolName())
		if name == "" {
			continue
		}
		byName[name] = handler
	}
	return &Registry{handlers: byName}
}

func NewDefaultRegistry() *Registry {
	return NewRegistry(
		NewAskUserQuestionHandler(),
		NewAskUserApprovalHandler(),
	)
}

func (r *Registry) Handler(toolName string) (Handler, bool) {
	if r == nil {
		return nil, false
	}
	handler, ok := r.handlers[normalizeToolName(toolName)]
	return handler, ok
}

func normalizeToolName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
