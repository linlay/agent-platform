package queryinput

type CoderModelOption struct {
	Key              string   `json:"key"`
	Name             string   `json:"name,omitempty"`
	Icon             string   `json:"icon,omitempty"`
	Provider         string   `json:"provider,omitempty"`
	ModelID          string   `json:"modelId,omitempty"`
	Protocol         string   `json:"protocol,omitempty"`
	IsReasoner       bool     `json:"isReasoner"`
	IsVision         bool     `json:"isVision"`
	ContextWindow    int      `json:"contextWindow,omitempty"`
	Timeout          int      `json:"timeout,omitempty"`
	ReasoningEfforts []string `json:"reasoningEfforts,omitempty"`
	ServiceTiers     []string `json:"serviceTiers,omitempty"`
}

type ChatErrorInfo struct {
	Code     string    `json:"code"`
	Message  string    `json:"message"`
	ChatID   string    `json:"chatId,omitempty"`
	RunIDs   []string  `json:"runIds,omitempty"`
	Awaiting *Awaiting `json:"awaiting,omitempty"`
}

type Awaiting struct {
	AwaitingID string `json:"awaitingId"`
	RunID      string `json:"runId"`
	Mode       string `json:"mode"`
	Status     string `json:"status"`
	CreatedAt  int64  `json:"createdAt"`
}

type ToolDefinition struct {
	Key           string         `json:"key"`
	Name          string         `json:"name"`
	Label         string         `json:"label,omitempty"`
	Description   string         `json:"description,omitempty"`
	AfterCallHint string         `json:"afterCallHint,omitempty"`
	Parameters    map[string]any `json:"parameters,omitempty"`
	OutputSchema  map[string]any `json:"outputSchema,omitempty"`
	Meta          map[string]any `json:"meta,omitempty"`
}
