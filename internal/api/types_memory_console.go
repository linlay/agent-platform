package api

type MemoryScopeSummary struct {
	ScopeType   string `json:"scopeType"`
	ScopeKey    string `json:"scopeKey"`
	Label       string `json:"label"`
	FileName    string `json:"fileName"`
	RecordCount int    `json:"recordCount"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type MemoryScopesResponse struct {
	AgentKey string               `json:"agentKey"`
	Scopes   []MemoryScopeSummary `json:"scopes"`
}

type MemoryMetaResponse struct {
	Categories  []string `json:"categories"`
	Types       []string `json:"types"`
	ScopeTypes  []string `json:"scopeTypes"`
	Statuses    []string `json:"statuses"`
	SourceTypes []string `json:"sourceTypes"`
}

type MemoryContextPreviewRequest struct {
	ChatID  string `json:"chatId"`
	Message string `json:"message"`
}

type MemoryContextPreviewSummary struct {
	StableCount      int            `json:"stableCount"`
	SessionCount     int            `json:"sessionCount"`
	ObservationCount int            `json:"observationCount"`
	StableChars      int            `json:"stableChars"`
	SessionChars     int            `json:"sessionChars"`
	ObservationChars int            `json:"observationChars"`
	DisclosedLayers  []string       `json:"disclosedLayers,omitempty"`
	StopReason       string         `json:"stopReason,omitempty"`
	SnapshotID       string         `json:"snapshotId,omitempty"`
	CandidateCounts  map[string]int `json:"candidateCounts,omitempty"`
	SelectedCounts   map[string]int `json:"selectedCounts,omitempty"`
}

type MemoryContextPreviewPrompts struct {
	Stable      string `json:"stable"`
	Session     string `json:"session"`
	Observation string `json:"observation"`
}

type MemoryContextPreviewContextSection struct {
	Order      int    `json:"order"`
	PromptType string `json:"promptType"`
	Role       string `json:"role"`
	Category   string `json:"category"`
	Source     string `json:"source"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	Chars      int    `json:"chars"`
}

type MemoryContextPreviewItem struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	ScopeType      string   `json:"scopeType"`
	ScopeKey       string   `json:"scopeKey"`
	Title          string   `json:"title"`
	Summary        string   `json:"summary"`
	Category       string   `json:"category"`
	Importance     int      `json:"importance"`
	Confidence     float64  `json:"confidence"`
	Status         string   `json:"status"`
	SourceType     string   `json:"sourceType"`
	Tags           []string `json:"tags,omitempty"`
	CreatedAt      int64    `json:"createdAt"`
	UpdatedAt      int64    `json:"updatedAt"`
	AccessCount    int      `json:"accessCount,omitempty"`
	LastAccessedAt *int64   `json:"lastAccessedAt,omitempty"`
	Order          int      `json:"order"`
}

type MemoryContextPreviewLayer struct {
	Layer          string                     `json:"layer"`
	CandidateCount int                        `json:"candidateCount"`
	SelectedCount  int                        `json:"selectedCount"`
	Chars          int                        `json:"chars"`
	Items          []MemoryContextPreviewItem `json:"items"`
}

type MemorySelectionScoreParts struct {
	Importance          float64 `json:"importance,omitempty"`
	EffectiveImportance float64 `json:"effectiveImportance,omitempty"`
	Decay               float64 `json:"decay,omitempty"`
	AccessBoost         float64 `json:"accessBoost,omitempty"`
	Recency             float64 `json:"recency,omitempty"`
	ScopeMatch          float64 `json:"scopeMatch,omitempty"`
	QueryMatch          float64 `json:"queryMatch,omitempty"`
	VectorScore         float64 `json:"vectorScore,omitempty"`
	ImportanceNorm      float64 `json:"importanceNorm,omitempty"`
	HybridCombined      float64 `json:"hybridCombined,omitempty"`
}

type MemorySelectionTrace struct {
	ID         string                    `json:"id"`
	Layer      string                    `json:"layer"`
	Selected   bool                      `json:"selected"`
	Score      float64                   `json:"score"`
	ScoreParts MemorySelectionScoreParts `json:"scoreParts"`
	Reason     string                    `json:"reason"`
}

type MemoryContextPreviewDecision struct {
	Layer   string                 `json:"layer"`
	Reason  string                 `json:"reason"`
	ItemIDs []string               `json:"itemIds"`
	Traces  []MemorySelectionTrace `json:"traces,omitempty"`
}

type MemoryContextPreviewResponse struct {
	Message   string                               `json:"message"`
	AgentKey  string                               `json:"agentKey"`
	ChatID    string                               `json:"chatId"`
	TeamID    string                               `json:"teamId,omitempty"`
	Enabled   bool                                 `json:"enabled"`
	Summary   MemoryContextPreviewSummary          `json:"summary"`
	Prompts   MemoryContextPreviewPrompts          `json:"prompts"`
	Layers    []MemoryContextPreviewLayer          `json:"layers"`
	Contexts  []MemoryContextPreviewContextSection `json:"contextSections,omitempty"`
	Decisions []MemoryContextPreviewDecision       `json:"decisions,omitempty"`
}

type MemoryScopeRecord struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Category   string   `json:"category"`
	Importance int      `json:"importance"`
	Confidence float64  `json:"confidence"`
	Status     string   `json:"status"`
	ScopeType  string   `json:"scopeType"`
	ScopeKey   string   `json:"scopeKey"`
	Tags       []string `json:"tags,omitempty"`
	CreatedAt  int64    `json:"createdAt"`
	UpdatedAt  int64    `json:"updatedAt"`
}

type MemoryScopeDetailMeta struct {
	Editable           bool `json:"editable"`
	RecordCount        int  `json:"recordCount"`
	GeneratedFromStore bool `json:"generatedFromStore"`
}

type MemoryScopeDetailResponse struct {
	AgentKey  string                `json:"agentKey"`
	ScopeType string                `json:"scopeType"`
	ScopeKey  string                `json:"scopeKey"`
	Label     string                `json:"label"`
	FileName  string                `json:"fileName"`
	Markdown  string                `json:"markdown"`
	Records   []MemoryScopeRecord   `json:"records"`
	Meta      MemoryScopeDetailMeta `json:"meta"`
}

type MemoryScopeRecordInput struct {
	ID         string   `json:"id,omitempty"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Category   string   `json:"category"`
	Importance int      `json:"importance"`
	Confidence float64  `json:"confidence"`
	Tags       []string `json:"tags,omitempty"`
}

type MemoryScopeSaveRequest struct {
	AgentKey       string                   `json:"agentKey"`
	ScopeType      string                   `json:"scopeType"`
	ScopeKey       string                   `json:"scopeKey,omitempty"`
	Mode           string                   `json:"mode"`
	Markdown       string                   `json:"markdown,omitempty"`
	Records        []MemoryScopeRecordInput `json:"records,omitempty"`
	ArchiveMissing bool                     `json:"archiveMissing,omitempty"`
}

type MemoryScopeSaveSummary struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Archived  int `json:"archived"`
	Unchanged int `json:"unchanged"`
}

type MemoryScopeSaveRecord struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	ScopeType string `json:"scopeType"`
	ScopeKey  string `json:"scopeKey"`
	UpdatedAt int64  `json:"updatedAt"`
}

type MemoryScopeSaveResponse struct {
	Saved     bool                    `json:"saved"`
	AgentKey  string                  `json:"agentKey"`
	ScopeType string                  `json:"scopeType"`
	ScopeKey  string                  `json:"scopeKey"`
	Summary   MemoryScopeSaveSummary  `json:"summary"`
	Records   []MemoryScopeSaveRecord `json:"records"`
	Markdown  string                  `json:"markdown"`
}

type MemoryScopeValidateRequest struct {
	AgentKey  string `json:"agentKey"`
	ScopeType string `json:"scopeType"`
	Markdown  string `json:"markdown"`
}

type MemoryScopeValidationIssue struct {
	Line    int    `json:"line"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

type MemoryScopeValidateResponse struct {
	Valid    bool                         `json:"valid"`
	Errors   []MemoryScopeValidationIssue `json:"errors,omitempty"`
	Warnings []MemoryScopeValidationIssue `json:"warnings,omitempty"`
}

type MemoryRecordsResponse struct {
	Count      int                    `json:"count"`
	NextCursor string                 `json:"nextCursor,omitempty"`
	Results    []StoredMemoryResponse `json:"results"`
}

type MemoryHistoryEvent struct {
	ID         string         `json:"id"`
	Timestamp  int64          `json:"ts"`
	AgentKey   string         `json:"agentKey,omitempty"`
	ChatID     string         `json:"chatId,omitempty"`
	RunID      string         `json:"runId,omitempty"`
	RequestID  string         `json:"requestId,omitempty"`
	UserKey    string         `json:"userKey,omitempty"`
	MemoryID   string         `json:"memoryId,omitempty"`
	MemoryKind string         `json:"memoryKind,omitempty"`
	ScopeType  string         `json:"scopeType,omitempty"`
	ScopeKey   string         `json:"scopeKey,omitempty"`
	Operation  string         `json:"operation"`
	Source     string         `json:"source,omitempty"`
	Status     string         `json:"status"`
	Before     map[string]any `json:"before,omitempty"`
	After      map[string]any `json:"after,omitempty"`
	Delta      map[string]any `json:"delta,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type MemoryHistoryResponse struct {
	Count      int                  `json:"count"`
	NextCursor string               `json:"nextCursor,omitempty"`
	Events     []MemoryHistoryEvent `json:"events"`
}

type MemoryRecordTimelineResponse struct {
	ID     string               `json:"id"`
	Events []MemoryHistoryEvent `json:"events"`
}

type MemoryRecordEmbedding struct {
	HasEmbedding bool   `json:"hasEmbedding"`
	Model        string `json:"model,omitempty"`
}

type MemoryRecordDetailResponse struct {
	ID          string                `json:"id"`
	SourceTable string                `json:"sourceTable"`
	Record      StoredMemoryResponse  `json:"record"`
	RawFields   map[string]any        `json:"rawFields,omitempty"`
	Embedding   MemoryRecordEmbedding `json:"embedding"`
}
