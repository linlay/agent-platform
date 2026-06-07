package frontendtools

import (
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

type Handler interface {
	ToolName() string
	ValidateArgs(args map[string]any) error
	BuildInitialAwaitAsk(toolID string, runID string, tool api.ToolDetailResponse, args map[string]any, chunkIndex int, timeout int64) *stream.AwaitAsk
	NormalizeSubmit(args map[string]any, params any) (map[string]any, error)
	FormatSubmitResult(format string, result contracts.ToolExecutionResult) (string, bool)
}

type Registry struct {
	handlers map[string]Handler
	fallback Handler
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
	registry := NewRegistry(
		NewAskUserQuestionHandler(),
	)
	registry.fallback = NewGenericFormHandler()
	return registry
}

func (r *Registry) Handler(toolName string) (Handler, bool) {
	if r == nil {
		return nil, false
	}
	handler, ok := r.handlers[normalizeToolName(toolName)]
	if ok {
		return handler, true
	}
	if r.fallback != nil {
		return r.fallback, true
	}
	return nil, false
}

func normalizeToolName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
