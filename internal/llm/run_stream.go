package llm

import (
	"bufio"
	"context"
	"io"
	"strings"
	"time"

	"agent-platform/internal/accesspolicy"
	"agent-platform/internal/api"
	"agent-platform/internal/bashsec"
	"agent-platform/internal/chat"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/hitl"
	. "agent-platform/internal/models"
)

type llmRunStream struct {
	engine             *LLMAgentEngine
	protocol           providerProtocol
	ctx                context.Context
	req                api.QueryRequest
	session            QuerySession
	runControl         *RunControl
	model              ModelDefinition
	provider           ProviderDefinition
	toolSpecs          []openAIToolSpec
	requestedToolNames []string
	messages           []openAIMessage
	protocolConfig     protocolRuntimeConfig
	stageSettings      StageSettings
	execCtx            *ExecutionContext
	maxSteps           int
	budgetStage        string
	toolChoice         string
	postToolHook       func(string, string) PostToolHookResult
	checker            hitl.Checker

	step                int
	stageToolCalls      int
	pending             []AgentDelta
	modelCall           *pendingModelCall
	modelTerminalError  error
	currentTurn         *providerTurnStream
	lastTrace           *llmChatTrace
	finished            bool
	closed              bool
	fallbackSent        bool
	cancelSent          bool
	finalTurnAttempted  bool
	allowToolUse        bool
	previousToolResult  any
	queuedToolCalls     []*preparedToolInvocation
	activeToolCall      *preparedToolInvocation
	activeToolBatch     *activeToolBatch
	stopAfterToolBatch  bool
	promptBuildOptions  PromptBuildOptions
	hitlPendingBatch    *pendingHITLApprovalBatch
	hitlPendingCall     *preparedToolInvocation
	hitlMatch           *hitl.InterceptResult
	hitlAwaitingID      string
	hitlAwaitArgs       map[string]any
	hitlRuleWhitelist   map[string]struct{}
	pendingHITLNotices  []hitlNoticeEntry
	skipPostToolHook    bool
	onApprovalSummary   func(chat.StepApproval)
	planningWrites      map[string]*planningWriteStreamState
	accessLevelVersion  int64
	systemInitCacheKey  string
	systemInitCacheUsed bool
	pendingSteerInputs  []map[string]any

	lastCallPromptTokens           int
	lastCallCompletionTokens       int
	lastCallTotalTokens            int
	lastCallCachedTokens           int
	lastCallReasoningTokens        int
	lastCallPromptCacheHitTokens   int
	lastCallPromptCacheMissTokens  int
	lastCallLLMChatCompletionCount int
	lastCallToolCallCount          int
	lastCallFirstTokenLatencyMs    int64
	lastCallGenerationDurationMs   int64
	runPromptTokens                int
	runCompletionTokens            int
	runTotalTokens                 int
	runCachedTokens                int
	runReasoningTokens             int
	runPromptCacheHitTokens        int
	runPromptCacheMissTokens       int
	runLLMChatCompletionCount      int
	runToolCallCount               int
	runFirstTokenLatencyTotalMs    int64
	runFirstTokenLatencyCount      int
	runGenerationDurationMs        int64
	lastSnapshotToolCallCount      int
	pendingUsageEmit               bool
	pendingTimingUsageEmit         bool
}

type providerTurnStream struct {
	body           io.ReadCloser
	cancel         context.CancelFunc
	reader         *bufio.Reader
	trace          *llmChatTrace
	content        strings.Builder
	reasoning      strings.Builder
	thinkTag       thinkTagParserState
	toolCalls      map[int]*toolCallAccumulator
	finishReason   string
	hasMeaningful  bool
	usage          *openAIUsage
	usageCommitted bool
	requestSentAt  time.Time
	firstVisibleAt time.Time
}

type pendingModelCall struct {
	prepared            preparedProviderRequest
	effectiveToolChoice string
	runSeq              int
	attempt             int
	maxAttempts         int
	logicalTurnCounted  bool
	attemptStartedAt    time.Time
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
	bashAccessReview    *accesspolicy.BashPlan
	fileAccessPlan      *filetools.AccessPlan
	fileWritePlan       *filetools.WritePlan
	approvalDecision    string
	hitlDecision        *hitlDecisionState
	queuedResult        *ToolExecutionResult
}

type pendingHITLApprovalBatch struct {
	awaitingID  string
	awaitArgs   map[string]any
	invocations []*preparedToolInvocation
	matches     []hitl.InterceptResult
	timeout     int
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
	toolName    string
	command     string
	decision    string
	ruleKey     string
	reason      string
	mode        string
	formPayload map[string]any
}

const defaultContextWindow = 128000
