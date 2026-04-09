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
type QueryLine struct {
	Type      string         `json:"_type"`
	ChatID    string         `json:"chatId"`
	RunID     string         `json:"runId"`
	UpdatedAt int64          `json:"updatedAt"`
	Hidden    *bool          `json:"hidden,omitempty"`
	Query     map[string]any `json:"query"`
}

// StepLine represents a _type:"step" line in chatId.jsonl.
type StepLine struct {
	Type      string          `json:"_type"`
	ChatID    string          `json:"chatId"`
	RunID     string          `json:"runId"`
	Stage     string          `json:"_stage"`
	Seq       int             `json:"_seq"`
	TaskID    string          `json:"taskId,omitempty"`
	UpdatedAt int64           `json:"updatedAt"`
	System    map[string]any  `json:"system,omitempty"`
	Plan      *PlanState      `json:"plan,omitempty"`
	Artifacts *ArtifactState  `json:"artifacts,omitempty"`
	Messages  []StoredMessage `json:"messages"`
}

// EventLine represents a _type:"event" line in chatId.jsonl.
type EventLine struct {
	Type      string         `json:"_type"`
	ChatID    string         `json:"chatId"`
	RunID     string         `json:"runId"`
	UpdatedAt int64          `json:"updatedAt"`
	Hidden    *bool          `json:"hidden,omitempty"`
	Event     map[string]any `json:"event"`
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
	ChatID         string `json:"chatId"`
	ChatName       string `json:"chatName"`
	AgentKey       string `json:"agentKey,omitempty"`
	TeamID         string `json:"teamId,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
	LastRunID      string `json:"lastRunId,omitempty"`
	LastRunContent string `json:"lastRunContent,omitempty"`
	ReadStatus     int    `json:"readStatus"`
	ReadAt         *int64 `json:"readAt,omitempty"`
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

type RunCompletion struct {
	ChatID          string
	RunID           string
	AssistantText   string
	InitialMessage  string
	UpdatedAtMillis int64
}
