package llm

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/models"
)

type AgentEngine = contracts.AgentEngine
type AgentStream = contracts.AgentStream
type QuerySession = contracts.QuerySession
type ExecutionContext = contracts.ExecutionContext
type ToolExecutionResult = contracts.ToolExecutionResult
type SandboxExecutionResult = contracts.SandboxExecutionResult
type Budget = contracts.Budget
type RetryPolicy = contracts.RetryPolicy
type PlanRuntimeState = contracts.PlanRuntimeState
type PlanTask = contracts.PlanTask
type StageSettings = contracts.StageSettings
type PlanExecuteSettings = contracts.PlanExecuteSettings
type PromptAppendConfig = contracts.PromptAppendConfig
type RuntimeRequestContext = contracts.RuntimeRequestContext
type AuthIdentity = contracts.AuthIdentity
type SandboxContext = contracts.SandboxContext
type AgentDigest = contracts.AgentDigest
type SandboxDigest = contracts.SandboxDigest
type LocalPaths = contracts.LocalPaths
type SandboxPaths = contracts.SandboxPaths
type ToolExecutor = contracts.ToolExecutor
type SandboxClient = contracts.SandboxClient
type RunControl = contracts.RunControl
type RunLoopState = contracts.RunLoopState
type AgentDelta = contracts.AgentDelta
type DeltaContent = contracts.DeltaContent
type DeltaReasoning = contracts.DeltaReasoning
type DeltaToolCall = contracts.DeltaToolCall
type DeltaToolEnd = contracts.DeltaToolEnd
type DeltaToolResult = contracts.DeltaToolResult
type DeltaStageMarker = contracts.DeltaStageMarker
type DeltaFinishReason = contracts.DeltaFinishReason
type DeltaError = contracts.DeltaError
type DeltaPlanUpdate = contracts.DeltaPlanUpdate
type DeltaTaskLifecycle = contracts.DeltaTaskLifecycle
type DeltaArtifactPublish = contracts.DeltaArtifactPublish
type DeltaRequestSubmit = contracts.DeltaRequestSubmit
type DeltaRequestSteer = contracts.DeltaRequestSteer
type DeltaRunCancel = contracts.DeltaRunCancel
type ModelRegistry = models.ModelRegistry
type ModelDefinition = models.ModelDefinition
type ProviderDefinition = models.ProviderDefinition
type ProtocolDefinition = models.ProtocolDefinition

const (
	RunLoopStateIdle           = contracts.RunLoopStateIdle
	RunLoopStateModelStreaming = contracts.RunLoopStateModelStreaming
	RunLoopStateToolExecuting  = contracts.RunLoopStateToolExecuting
	RunLoopStateWaitingSubmit  = contracts.RunLoopStateWaitingSubmit
	RunLoopStateCompleted      = contracts.RunLoopStateCompleted
	RunLoopStateCancelled      = contracts.RunLoopStateCancelled
	RunLoopStateFailed         = contracts.RunLoopStateFailed

	ErrorScopeRun            = contracts.ErrorScopeRun
	ErrorScopeTask           = contracts.ErrorScopeTask
	ErrorScopeTool           = contracts.ErrorScopeTool
	ErrorScopeModel          = contracts.ErrorScopeModel
	ErrorScopeFrontendSubmit = contracts.ErrorScopeFrontendSubmit

	ErrorCategorySystem    = contracts.ErrorCategorySystem
	ErrorCategoryTimeout   = contracts.ErrorCategoryTimeout
	ErrorCategoryInterrupt = contracts.ErrorCategoryInterrupt
	ErrorCategoryTool      = contracts.ErrorCategoryTool
	ErrorCategoryModel     = contracts.ErrorCategoryModel
)

var (
	ErrRunInterrupted               = contracts.ErrRunInterrupted
	ErrRunFinished                  = contracts.ErrRunFinished
	ErrRunControlUnavailable        = contracts.ErrRunControlUnavailable
	ErrFrontendToolMissingToolID    = contracts.ErrFrontendToolMissingToolID
	ErrFrontendSubmitAlreadyWaiting = contracts.ErrFrontendSubmitAlreadyWaiting
	ErrToolArgsTemplateMissingValue = contracts.ErrToolArgsTemplateMissingValue
	ErrBudgetExceeded               = contracts.ErrBudgetExceeded
)

func anyIntNode(value any) int {
	return contracts.AnyIntNode(value)
}

func anyBoolNode(value any) bool {
	return contracts.AnyBoolNode(value)
}

func anyStringNode(value any) string {
	return contracts.AnyStringNode(value)
}

func anyMapNode(value any) map[string]any {
	return contracts.AnyMapNode(value)
}

func anyListStrings(value any) []string {
	return contracts.AnyListStrings(value)
}

func DefaultPromptAppendConfig() contracts.PromptAppendConfig {
	return contracts.DefaultPromptAppendConfig()
}

func ResolvePlanExecuteSettings(raw map[string]any, defaultsMaxSteps int, defaultsMaxWorkRounds int) contracts.PlanExecuteSettings {
	return contracts.ResolvePlanExecuteSettings(raw, defaultsMaxSteps, defaultsMaxWorkRounds)
}

func NewErrorPayload(code string, message string, scope string, category contracts.ErrorCategory, diagnostics map[string]any) map[string]any {
	return contracts.NewErrorPayload(code, message, scope, category, diagnostics)
}

func RunControlFromContext(ctx context.Context) *RunControl {
	return contracts.RunControlFromContext(ctx)
}

func normalizeBudget(b Budget) Budget {
	return contracts.NormalizeBudget(b)
}

func normalizeRetryPolicy(policy RetryPolicy, fallback RetryPolicy) RetryPolicy {
	if policy.MaxCalls <= 0 {
		policy.MaxCalls = fallback.MaxCalls
	}
	if policy.TimeoutMs <= 0 {
		policy.TimeoutMs = fallback.TimeoutMs
	}
	if policy.RetryCount < 0 {
		policy.RetryCount = 0
	}
	return policy
}

func runTimeout(b Budget) time.Duration {
	return time.Duration(maxInt(b.RunTimeoutMs, 1)) * time.Millisecond
}

func toolTimeout(policy RetryPolicy) time.Duration {
	return time.Duration(maxInt(policy.TimeoutMs, 1)) * time.Millisecond
}

func structuredOrOutput(result ToolExecutionResult) any {
	if len(result.Structured) > 0 {
		return result.Structured
	}
	return result.Output
}

func maxInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func normalizePlanTaskStatus(raw string) string {
	return contracts.NormalizePlanTaskStatus(raw)
}

func cloneAnyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneToolDefinition(def api.ToolDetailResponse) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:           def.Key,
		Name:          def.Name,
		Label:         def.Label,
		Description:   def.Description,
		AfterCallHint: def.AfterCallHint,
		Parameters:    cloneAnyMap(def.Parameters),
		Meta:          cloneAnyMap(def.Meta),
	}
}

func planTasksArray(state *PlanRuntimeState) []map[string]any {
	return contracts.PlanTasksArray(state)
}

func defaultEndpointPath(protocol string, baseURL string) string {
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "ANTHROPIC":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/messages"
		}
		return "/v1/messages"
	case "", "OPENAI":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/chat/completions"
		}
		return "/v1/chat/completions"
	default:
		return ""
	}
}

func normalizedBasePath(rawBaseURL string) string {
	parsed, err := urlParse(strings.TrimSpace(rawBaseURL))
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(parsed.EscapedPath())
	if path == "" {
		path = strings.TrimSpace(parsed.Path)
	}
	if path == "" || path == "/" {
		return ""
	}
	return "/" + strings.Trim(strings.TrimSpace(path), "/")
}

var previousResultPattern = regexp.MustCompile(`\$\{previousResult\.([a-zA-Z0-9_.-]+)\}`)

func ExpandToolArgsTemplates(input any, previousResult any) (any, error) {
	switch value := input.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			expanded, err := ExpandToolArgsTemplates(item, previousResult)
			if err != nil {
				return nil, err
			}
			out[key] = expanded
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(value))
		for _, item := range value {
			expanded, err := ExpandToolArgsTemplates(item, previousResult)
			if err != nil {
				return nil, err
			}
			out = append(out, expanded)
		}
		return out, nil
	case string:
		return expandTemplateString(value, previousResult)
	default:
		return input, nil
	}
}

func expandTemplateString(value string, previousResult any) (any, error) {
	matches := previousResultPattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value, nil
	}
	if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(value) {
		resolved, err := resolvePreviousResultPath(value[matches[0][2]:matches[0][3]], previousResult)
		if err != nil {
			return nil, err
		}
		return resolved, nil
	}

	var builder strings.Builder
	last := 0
	for _, match := range matches {
		builder.WriteString(value[last:match[0]])
		resolved, err := resolvePreviousResultPath(value[match[2]:match[3]], previousResult)
		if err != nil {
			return nil, err
		}
		builder.WriteString(fmt.Sprint(resolved))
		last = match[1]
	}
	builder.WriteString(value[last:])
	return builder.String(), nil
}

func resolvePreviousResultPath(path string, previousResult any) (any, error) {
	current := previousResult
	for _, segment := range strings.Split(strings.TrimSpace(path), ".") {
		if segment == "" {
			continue
		}
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrToolArgsTemplateMissingValue, path)
		}
		next, ok := asMap[segment]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrToolArgsTemplateMissingValue, path)
		}
		current = next
	}
	return current, nil
}

func urlParse(raw string) (*url.URL, error) {
	return url.Parse(raw)
}
