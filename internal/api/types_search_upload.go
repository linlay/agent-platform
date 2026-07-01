package api

type GlobalSearchRequest struct {
	Query    string `json:"query"`
	AgentKey string `json:"agentKey,omitempty"`
	TeamID   string `json:"teamId,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type GlobalSearchResult struct {
	ChatID    string `json:"chatId"`
	ChatName  string `json:"chatName"`
	AgentKey  string `json:"agentKey,omitempty"`
	TeamID    string `json:"teamId,omitempty"`
	RunID     string `json:"runId,omitempty"`
	Kind      string `json:"kind"`
	Role      string `json:"role,omitempty"`
	Timestamp int64  `json:"timestamp"`
	Snippet   string `json:"snippet"`
	Score     int    `json:"score"`
}

type GlobalSearchResponse struct {
	Query   string               `json:"query"`
	Count   int                  `json:"count"`
	Results []GlobalSearchResult `json:"results"`
}

type UploadResponse struct {
	RequestID string       `json:"requestId"`
	ChatID    string       `json:"chatId"`
	Upload    UploadTicket `json:"upload"`
}

type UploadTicket struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	MimeType  string `json:"mimeType,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256,omitempty"`
}
