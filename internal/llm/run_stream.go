package llm

import (
	"bufio"
	"context"
	"io"
	"strings"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/bashsec"
	"agent-platform-runner-go/internal/chat"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/filetools"
	"agent-platform-runner-go/internal/hitl"
	. "agent-platform-runner-go/internal/models"
)

type llmRunStream struct {
	engine              *LLMAgentEngine
	protocol            providerProtocol
	ctx                 context.Context
	req                 api.QueryRequest
	session             QuerySession
	runControl          *RunControl
	model               ModelDefinition
	provider            ProviderDefinition
	toolSpecs           []openAIToolSpec
	requestedToolNames  []string
	messages            []openAIMessage
	protocolConfig      protocolRuntimeConfig
	stageSettings       StageSettings
	execCtx             *ExecutionContext
	maxSteps            int
	toolChoice          string
	maxToolCallsPerTurn int
	postToolHook        func(string, string) PostToolHookResult
	checker             hitl.Checker

	step               int
	pending            []AgentDelta
	currentTurn        *providerTurnStream
	finished           bool
	closed             bool
	fallbackSent       bool
	cancelSent         bool
	finalTurnAttempted bool
	allowToolUse       bool
	finalTurnSystem    string
	previousToolResult any
	queuedToolCalls    []*preparedToolInvocation
	activeToolCall     *preparedToolInvocation
	promptBuildOptions PromptBuildOptions
	hitlPendingBatch   *pendingHITLApprovalBatch
	hitlPendingCall    *preparedToolInvocation
	hitlMatch          *hitl.InterceptResult
	hitlAwaitingID     string
	hitlAwaitArgs      map[string]any
	hitlRuleWhitelist  map[string]struct{}
	pendingHITLNotices []hitlNoticeEntry
	skipPostToolHook   bool
	onApprovalSummary  func(chat.StepApproval)

	lastCallPromptTokens     int
	lastCallCompletionTokens int
	lastCallTotalTokens      int
	runPromptTokens          int
	runCompletionTokens      int
	runTotalTokens           int
	pendingUsageEmit         bool
}

type providerTurnStream struct {
	body          io.ReadCloser
	reader        *bufio.Reader
	content       strings.Builder
	reasoning     strings.Builder
	thinkTag      thinkTagParserState
	toolCalls     map[int]*toolCallAccumulator
	finishReason  string
	hasMeaningful bool
}

type thinkTagParserState struct {
	buffer  strings.Builder
	inThink bool
}

type toolCallAccumulator struct {
	ID           string
	Type         string
	FunctionName string
	Arguments    strings.Builder
	EmittedBytes int
}

type preparedToolInvocation struct {
	toolID              string
	toolName            string
	args                map[string]any
	prelude             []AgentDelta
	awaitExternalResult bool
	toolCallCounted     bool
	precheckedHITL      *hitl.InterceptResult
	bashSecurityReview  *bashsec.ReviewResult
	fileWritePlan       *filetools.WritePlan
	approvalID          string
	approvalDecision    string
	hitlDecision        *hitlDecisionState
	queuedResult        *ToolExecutionResult
}

type pendingHITLApprovalBatch struct {
	awaitingID  string
	awaitArgs   map[string]any
	invocations []*preparedToolInvocation
	timeoutMs   int
}

type hitlDecisionState struct {
	AwaitingID  string
	Decision    string
	Reason      string
	RuleKey     string
	Scope       string
	Executed    bool
	Mode        string
	FormPayload map[string]any
}

type hitlNoticeEntry struct {
	toolID      string
	command     string
	decision    string
	ruleKey     string
	reason      string
	mode        string
	formPayload map[string]any
}

// PostToolHookResult controls what happens after a tool call.
type PostToolHookResult int

const (
	PostToolContinue PostToolHookResult = iota
	PostToolStop
)

const defaultContextWindow = 128000
