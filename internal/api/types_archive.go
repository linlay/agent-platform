package api

type ArchiveChatRequest struct {
	ChatIDs []string `json:"chatIds"`
}

type ArchiveChatResult struct {
	ChatID  string `json:"chatId"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

type ArchiveChatResponse struct {
	Results []ArchiveChatResult `json:"results"`
}

type ArchivesRequest struct {
	AgentKey string `json:"agentKey,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Offset   int    `json:"offset,omitempty"`
}

type ArchivedSummaryResponse struct {
	ChatID         string         `json:"chatId"`
	ChatName       string         `json:"chatName"`
	AgentKey       string         `json:"agentKey,omitempty"`
	TeamID         string         `json:"teamId,omitempty"`
	CreatedAt      int64          `json:"createdAt"`
	UpdatedAt      int64          `json:"updatedAt"`
	LastRunAt      int64          `json:"lastRunAt"`
	ArchivedAt     int64          `json:"archivedAt"`
	LastRunID      string         `json:"lastRunId,omitempty"`
	LastRunContent string         `json:"lastRunContent,omitempty"`
	HasAttachments bool           `json:"hasAttachments"`
	Usage          *ChatUsageData `json:"usage,omitempty"`
}

type ArchivesResponse struct {
	Total int                       `json:"total"`
	Items []ArchivedSummaryResponse `json:"items"`
}

type ArchiveSearchRequest struct {
	Query    string `json:"query"`
	AgentKey string `json:"agentKey,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type ArchiveSearchResult struct {
	ChatID         string `json:"chatId"`
	ChatName       string `json:"chatName"`
	AgentKey       string `json:"agentKey,omitempty"`
	TeamID         string `json:"teamId,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
	LastRunAt      int64  `json:"lastRunAt"`
	LastRunID      string `json:"lastRunId,omitempty"`
	LastRunContent string `json:"lastRunContent,omitempty"`
	ArchivedAt     int64  `json:"archivedAt"`
	Snippet        string `json:"snippet"`
	Score          int    `json:"score"`
}

type ArchiveSearchResponse struct {
	Query   string                `json:"query"`
	Count   int                   `json:"count"`
	Results []ArchiveSearchResult `json:"results"`
}

type ArchiveDeleteRequest struct {
	ChatID string `json:"chatId"`
}

type ArchiveDeleteResponse struct {
	ChatID  string `json:"chatId"`
	Deleted bool   `json:"deleted"`
}

type ArchiveRestoreRequest struct {
	ChatIDs []string `json:"chatIds"`
}

type ArchiveRestoreResult struct {
	ChatID  string               `json:"chatId"`
	Success bool                 `json:"success"`
	Error   string               `json:"error,omitempty"`
	Summary *ChatSummaryResponse `json:"summary,omitempty"`
}

type ArchiveRestoreResponse struct {
	Results []ArchiveRestoreResult `json:"results"`
}
