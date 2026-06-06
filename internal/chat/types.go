package chat

import "agent-platform/internal/stream"

const (
	LegacyToolResultsDirName = ".tool-results"
	ToolRootDirName          = ".tools"
	ToolResultsDirName       = "results"
	ToolStateDirName         = "state"
	ToolPlansDirName         = "plans"
	FileVersionsFileName     = "file-versions.json"
)

// ---------------------------------------------------------------------------
// Plan / Artifact state (shared by step lines and API responses)
// ---------------------------------------------------------------------------

type PlanState struct {
	PlanID string          `json:"planId"`
	Tasks  []PlanTaskState `json:"tasks"`
}

type PlanTaskState struct {
	TaskID      string `json:"taskId"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type ArtifactState struct {
	Items []ArtifactItemState `json:"items,omitempty"`
}

type ArtifactItemState struct {
	ArtifactID string `json:"artifactId"`
	Type       string `json:"type"`
	Name       string `json:"name"`
	MimeType   string `json:"mimeType,omitempty"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	URL        string `json:"url,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

type PlanningState struct {
	PlanningID   string `json:"planningId"`
	PlanningFile string `json:"planningFile"`
	Markdown     string `json:"text,omitempty"`
}

// ---------------------------------------------------------------------------
// Chat Storage V3.1 — JSONL line types (matching Java format)
// ---------------------------------------------------------------------------

type SystemInitLine struct {
	Type          string         `json:"_type"`
	ChatID        string         `json:"chatId"`
	AgentKey      string         `json:"agentKey"`
	RunID         string         `json:"runId"`
	CreatedAt     int64          `json:"createdAt"`
	Fingerprint   string         `json:"fingerprint"`
	CacheKey      string         `json:"cacheKey,omitempty"`
	Mode          string         `json:"mode,omitempty"`
	Stage         string         `json:"stage,omitempty"`
	SystemMessage map[string]any `json:"systemMessage"`
	Tools         []any          `json:"tools"`
}

type QueryLineSystemInit struct {
	CacheKey      string         `json:"cacheKey"`
	Fingerprint   string         `json:"fingerprint"`
	SystemMessage map[string]any `json:"systemMessage"`
	Tools         []any          `json:"tools"`
}

// QueryLine represents a _type:"query" line in chatId.jsonl.
// Field order matches Java: chatId, runId, updatedAt, query, systems, _type.
type QueryLine struct {
	ChatID      string                `json:"chatId"`
	RunID       string                `json:"runId"`
	UpdatedAt   int64                 `json:"updatedAt"`
	TaskID      string                `json:"taskId,omitempty"`
	TaskName    string                `json:"taskName,omitempty"`
	TaskToolID  string                `json:"taskToolId,omitempty"`
	SubAgentKey string                `json:"subAgentKey,omitempty"`
	Query       map[string]any        `json:"query"`
	Systems     []QueryLineSystemInit `json:"systems,omitempty"`
	Type        string                `json:"_type"`
}

// StepLine represents a step line in chatId.jsonl.
// _type is the persisted step shape: "react" or "plan-execute".
// REACT/ONESHOT/CODER modes: { _type: "react", seq: N, messages: [...] }
// In react lines, seq is the model-call grouping id, not a physical line
// number. Continuation lines such as HITL-split tool results may reuse the
// same seq as the assistant tool-call step that caused them.
// PLAN_EXECUTE mode:
//
//	{ _type: "plan-execute", stage: "plan", messages: [...] }
//	{ _type: "plan-execute", stage: "execute", seq: N, messages: [...] }
//	{ _type: "plan-execute", stage: "summary", messages: [...] }
type StepLine struct {
	ChatID          string           `json:"chatId"`
	RunID           string           `json:"runId"`
	UpdatedAt       int64            `json:"updatedAt"`
	TaskID          string           `json:"taskId,omitempty"`
	TaskStatus      string           `json:"taskStatus,omitempty"`
	TaskSubAgentKey string           `json:"taskSubAgentKey,omitempty"`
	System          map[string]any   `json:"system,omitempty"`
	SystemRef       map[string]any   `json:"systemRef,omitempty"`
	Debug           map[string]any   `json:"debug,omitempty"`
	Messages        []StoredMessage  `json:"messages"`
	Awaiting        []map[string]any `json:"awaiting,omitempty"`
	Usage           map[string]any   `json:"usage,omitempty"`
	ContextWindow   map[string]any   `json:"contextWindow,omitempty"`
	Type            string           `json:"_type"`
	Stage           string           `json:"stage,omitempty"`
	Seq             int              `json:"seq,omitempty"`
	Plan            *PlanState       `json:"plan,omitempty"`
	Artifacts       *ArtifactState   `json:"artifacts,omitempty"`
}

type StepApproval struct {
	Summary   string                 `json:"summary"`
	Notice    string                 `json:"-"`
	Decisions []StepApprovalDecision `json:"decisions,omitempty"`
}

type StepApprovalDecision struct {
	ToolID   string         `json:"toolId"`
	Command  string         `json:"command"`
	Decision string         `json:"decision"`
	RuleKey  string         `json:"ruleKey,omitempty"`
	Reason   string         `json:"reason,omitempty"`
	Mode     string         `json:"mode,omitempty"`
	Payload  map[string]any `json:"payload,omitempty"`
}

type EventLine struct {
	ChatID    string         `json:"chatId"`
	RunID     string         `json:"runId"`
	UpdatedAt int64          `json:"updatedAt"`
	Event     map[string]any `json:"event"`
	Type      string         `json:"_type"`
}

type SubmitLine struct {
	ChatID    string         `json:"chatId"`
	RunID     string         `json:"runId"`
	UpdatedAt int64          `json:"updatedAt"`
	Submit    map[string]any `json:"submit,omitempty"`
	Answer    map[string]any `json:"answer,omitempty"`
	Type      string         `json:"_type"`
}

// ---------------------------------------------------------------------------
// StoredMessage — one message inside a StepLine.messages array
// ---------------------------------------------------------------------------

type StoredMessage struct {
	Role             string           `json:"role"`
	Content          []ContentPart    `json:"content,omitempty"`
	Approval         *StepApproval    `json:"approval,omitempty"`
	ReasoningContent []ContentPart    `json:"reasoning_content,omitempty"`
	ToolCalls        []StoredToolCall `json:"tool_calls,omitempty"`
	Name             string           `json:"name,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	Ts               *int64           `json:"ts,omitempty"`
	ReasoningID      string           `json:"_reasoningId,omitempty"`
	ContentID        string           `json:"_contentId,omitempty"`
	MsgID            string           `json:"_msgId,omitempty"`
	ToolID           string           `json:"_toolId,omitempty"`
	ActionID         string           `json:"_actionId,omitempty"`
}

type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type StoredToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function StoredFunction `json:"function"`
}

type StoredFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ---------------------------------------------------------------------------
// Index / summary / detail types
// ---------------------------------------------------------------------------

type Summary struct {
	ChatID          string           `json:"chatId"`
	ChatName        string           `json:"chatName"`
	AgentKey        string           `json:"agentKey,omitempty"`
	TeamID          string           `json:"teamId,omitempty"`
	SourceChannel   string           `json:"sourceChannel,omitempty"`
	CreatedAt       int64            `json:"createdAt"`
	UpdatedAt       int64            `json:"updatedAt"`
	LastRunID       string           `json:"lastRunId,omitempty"`
	LastRunContent  string           `json:"lastRunContent,omitempty"`
	Read            ChatReadState    `json:"read"`
	PendingAwaiting *PendingAwaiting `json:"pendingAwaiting,omitempty"`
	Usage           *UsageData       `json:"usage,omitempty"`
}

type ChatReadState struct {
	IsRead    bool   `json:"isRead"`
	ReadAt    *int64 `json:"readAt,omitempty"`
	ReadRunID string `json:"readRunId,omitempty"`
}

type AgentChatStats struct {
	TotalCount  int `json:"totalCount"`
	UnreadCount int `json:"unreadCount"`
}

type PendingAwaiting struct {
	AwaitingID string `json:"awaitingId"`
	RunID      string `json:"runId"`
	Mode       string `json:"mode"`
	CreatedAt  int64  `json:"createdAt"`
}

type PendingAwaitingWithChat struct {
	ChatID     string
	AwaitingID string
	RunID      string
	Mode       string
	CreatedAt  int64
}

type PersistedAwaitingAsk struct {
	AwaitingID string
	RunID      string
	Mode       string
	Payload    map[string]any
}

type PersistedAwaitingSubmit struct {
	ChatID     string
	RunID      string
	AwaitingID string
	SubmitID   string
	UpdatedAt  int64
	Submit     map[string]any
	Answer     map[string]any
}

type Detail struct {
	ChatID        string
	ChatName      string
	RawMessages   []map[string]any
	Events        []stream.EventData
	ContextWindow map[string]any
	ReplayUsage   ReplayUsage
	References    []map[string]any
	Plan          *PlanState
	Planning      *PlanningState
	Artifact      *ArtifactState
}

type ReplayUsage struct {
	LastRunID string
	LastRun   UsageData
	Chat      UsageData
}

type UsageData struct {
	ModelKey               string  `json:"-"`
	PromptTokens           int     `json:"promptTokens"`
	CompletionTokens       int     `json:"completionTokens"`
	TotalTokens            int     `json:"totalTokens"`
	CachedTokens           int     `json:"-"`
	ReasoningTokens        int     `json:"-"`
	PromptCacheHitTokens   int     `json:"-"`
	PromptCacheMissTokens  int     `json:"-"`
	EstimatedCostCurrency  string  `json:"-"`
	EstimatedCostInputHit  float64 `json:"-"`
	EstimatedCostInputMiss float64 `json:"-"`
	EstimatedCostOutput    float64 `json:"-"`
	EstimatedCostTotal     float64 `json:"-"`
	LlmChatCompletionCount int     `json:"-"`
	ToolCallCount          int     `json:"-"`
}

type RunCompletion struct {
	ChatID          string
	RunID           string
	AgentKey        string
	AssistantText   string
	InitialMessage  string
	FinishReason    string
	StartedAtMillis int64
	UpdatedAtMillis int64
	Usage           UsageData
}

type RunSummary struct {
	RunID           string
	ChatID          string
	AgentKey        string
	InitialMessage  string
	AssistantText   string
	FinishReason    string
	StartedAt       int64
	CompletedAt     int64
	Usage           UsageData
	FeedbackType    string
	FeedbackComment string
	FeedbackAt      int64
}

type GlobalSearchHit struct {
	Kind      string
	ChatID    string
	ChatName  string
	AgentKey  string
	TeamID    string
	RunID     string
	Stage     string
	Role      string
	Timestamp int64
	Snippet   string
	Score     int
}

type RunTrace struct {
	ChatID        string
	ChatName      string
	AgentKey      string
	TeamID        string
	RunID         string
	Query         *QueryLine
	Steps         []StepLine
	AssistantText string
}
