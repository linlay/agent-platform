package chat

type PlanState struct {
	PlanID string          `json:"planId"`
	Tasks  []PlanTaskState `json:"tasks,omitempty"`
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
	Events      []map[string]any
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
