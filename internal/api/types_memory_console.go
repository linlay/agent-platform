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
