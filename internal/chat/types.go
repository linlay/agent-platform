package chat

import "agent-platform-runner-go/internal/stream"

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

// ---------------------------------------------------------------------------
// Chat Storage V3.1 — JSONL line types (matching Java format)
// ---------------------------------------------------------------------------

// QueryLine represents a _type:"query" line in chatId.jsonl.
// Field order matches Java: chatId, runId, updatedAt, hidden, query, _type.
type QueryLine struct {
	ChatID    string         `json:"chatId"`
	RunID     string         `json:"runId"`
	UpdatedAt int64          `json:"updatedAt"`
	Hidden    bool           `json:"hidden,omitempty"`
	Query     map[string]any `json:"query"`
	Type      string         `json:"_type"`
}

// StepLine represents a step line in chatId.jsonl.
// _type is the agent mode: "react" or "plan-execute".
// REACT mode: { _type: "react", seq: N, messages: [...] }
// PLAN_EXECUTE mode:
//
//	{ _type: "plan-execute", stage: "plan", messages: [...] }
//	{ _type: "plan-execute", stage: "execute", seq: N, messages: [...] }
//	{ _type: "plan-execute", stage: "summary", messages: [...] }
type StepLine struct {
	ChatID    string          `json:"chatId"`
	RunID     string          `json:"runId"`
	UpdatedAt int64           `json:"updatedAt"`
	TaskID    string          `json:"taskId,omitempty"`
	System    map[string]any  `json:"system,omitempty"`
	Messages  []StoredMessage `json:"messages"`
	Type      string          `json:"_type"`
	Stage     string          `json:"stage,omitempty"`
	Seq       int             `json:"seq,omitempty"`
	Plan      *PlanState      `json:"plan,omitempty"`
	Artifacts *ArtifactState  `json:"artifacts,omitempty"`
}

type EventLine struct {
	ChatID    string         `json:"chatId"`
	RunID     string         `json:"runId"`
	UpdatedAt int64          `json:"updatedAt"`
	Event     map[string]any `json:"event"`
	Type      string         `json:"_type"`
}

// ---------------------------------------------------------------------------
// StoredMessage — one message inside a StepLine.messages array
// ---------------------------------------------------------------------------

type StoredMessage struct {
	Role             string           `json:"role"`
	Content          []ContentPart    `json:"content,omitempty"`
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
	ChatID         string     `json:"chatId"`
	ChatName       string     `json:"chatName"`
	AgentKey       string     `json:"agentKey,omitempty"`
	TeamID         string     `json:"teamId,omitempty"`
	CreatedAt      int64      `json:"createdAt"`
	UpdatedAt      int64      `json:"updatedAt"`
	LastRunID      string     `json:"lastRunId,omitempty"`
	LastRunContent string     `json:"lastRunContent,omitempty"`
	ReadStatus     int        `json:"readStatus"`
	ReadAt         *int64     `json:"readAt,omitempty"`
	Usage          *UsageData `json:"usage,omitempty"`
}

type Detail struct {
	ChatID      string
	ChatName    string
	RawMessages []map[string]any
	Events      []stream.EventData
	References  []map[string]any
	Plan        *PlanState
	Artifact    *ArtifactState
}

type UsageData struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

type RunCompletion struct {
	ChatID          string
	RunID           string
	AssistantText   string
	InitialMessage  string
	UpdatedAtMillis int64
	Usage           UsageData
}
