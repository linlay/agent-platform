package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"agent-platform/internal/stream"
)

type ApiResponse[T any] struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data T      `json:"data"`
}

type FileHistoryResponse struct {
	Content string `json:"content"`
}

func Success[T any](data T) ApiResponse[T] {
	return ApiResponse[T]{
		Code: 0,
		Msg:  "success",
		Data: data,
	}
}

func Failure(code int, msg string) ApiResponse[map[string]any] {
	return ApiResponse[map[string]any]{
		Code: code,
		Msg:  msg,
		Data: map[string]any{},
	}
}

const (
	QueryRoleUser       = "user"
	QueryRoleAssistant  = "assistant"
	QueryRoleAutomation = "automation"
	QueryRoleSystem     = "system"

	QueryRoleValidationMessage = "role must be user, assistant, automation, or system"
)

func NormalizeQueryRole(role string) (string, bool) {
	switch strings.TrimSpace(role) {
	case "", QueryRoleUser:
		return QueryRoleUser, true
	case QueryRoleAssistant:
		return QueryRoleAssistant, true
	case QueryRoleAutomation:
		return QueryRoleAutomation, true
	case QueryRoleSystem:
		return QueryRoleSystem, true
	default:
		return "", false
	}
}

func DefaultQueryRole(role string) string {
	normalized, ok := NormalizeQueryRole(role)
	if !ok {
		return ""
	}
	return normalized
}

func QueryRoleVisible(role string) bool {
	normalized, ok := NormalizeQueryRole(role)
	if !ok {
		return true
	}
	return normalized != QueryRoleAutomation && normalized != QueryRoleSystem
}

func ProviderSafeQueryMessage(role string, message string) (string, string) {
	normalized, ok := NormalizeQueryRole(role)
	if !ok {
		normalized = QueryRoleUser
	}
	message = strings.TrimSpace(message)
	switch normalized {
	case QueryRoleAutomation:
		return QueryRoleUser, "[automation request]\n" + message
	case QueryRoleSystem:
		return QueryRoleUser, "[system request]\n" + message
	case QueryRoleAssistant:
		return QueryRoleAssistant, message
	default:
		return QueryRoleUser, message
	}
}

type QueryRequest struct {
	RequestID    string             `json:"requestId,omitempty"`
	RunID        string             `json:"runId,omitempty"`
	ChatID       string             `json:"chatId,omitempty"`
	AgentKey     string             `json:"agentKey,omitempty"`
	TeamID       string             `json:"teamId,omitempty"`
	Role         string             `json:"role,omitempty"`
	Message      string             `json:"message"`
	References   []Reference        `json:"references,omitempty"`
	Params       map[string]any     `json:"params,omitempty"`
	Scene        *Scene             `json:"scene,omitempty"`
	Stream       *bool              `json:"stream,omitempty"`
	PlanningMode *bool              `json:"planningMode,omitempty"`
	AccessLevel  string             `json:"accessLevel,omitempty"`
	Model        *QueryModelOptions `json:"model,omitempty"`
}

type QueryModelOptions struct {
	Key             string `json:"key,omitempty"`
	ModelID         string `json:"modelId,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

type Scene struct {
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
}

type Reference struct {
	ID          string         `json:"id,omitempty"`
	Type        string         `json:"type,omitempty"`
	Name        string         `json:"name,omitempty"`
	MimeType    string         `json:"mimeType,omitempty"`
	SizeBytes   *int64         `json:"sizeBytes,omitempty"`
	URL         string         `json:"url,omitempty"`
	SHA256      string         `json:"sha256,omitempty"`
	SandboxPath string         `json:"sandboxPath,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

type SubmitRequest struct {
	ChatID     string       `json:"chatId,omitempty"`
	RunID      string       `json:"runId"`
	AgentKey   string       `json:"agentKey"`
	AwaitingID string       `json:"awaitingId"`
	SubmitID   string       `json:"submitId,omitempty"`
	Params     SubmitParams `json:"params"`
}

type SubmitParams []json.RawMessage

func (p *SubmitParams) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("params must be an array")
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("params must be an array")
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return fmt.Errorf("params must be an array")
	}
	*p = SubmitParams(raw)
	return nil
}

func (p SubmitParams) MarshalJSON() ([]byte, error) {
	return json.Marshal([]json.RawMessage(p))
}

func (p SubmitParams) Empty() bool {
	return len(p) == 0
}

func DecodeSubmitParam(raw json.RawMessage) (map[string]any, error) {
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, fmt.Errorf("submit items must be objects")
	}
	if len(item) == 0 {
		return nil, fmt.Errorf("submit items must be objects")
	}
	return item, nil
}

func DecodeSubmitParams(params SubmitParams) ([]map[string]any, error) {
	if len(params) == 0 {
		return nil, nil
	}
	items := make([]map[string]any, 0, len(params))
	for _, raw := range params {
		item, err := DecodeSubmitParam(raw)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func EncodeSubmitParams(value any) (SubmitParams, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var params SubmitParams
	if err := json.Unmarshal(data, &params); err != nil {
		return nil, err
	}
	return params, nil
}

type SubmitResponse struct {
	Accepted   bool   `json:"accepted"`
	Status     string `json:"status"`
	ChatID     string `json:"chatId,omitempty"`
	RunID      string `json:"runId"`
	AwaitingID string `json:"awaitingId"`
	SubmitID   string `json:"submitId,omitempty"`
	Continued  bool   `json:"continued,omitempty"`
	Detail     string `json:"detail"`
}

type SteerRequest struct {
	RequestID string `json:"requestId,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	RunID     string `json:"runId"`
	SteerID   string `json:"steerId,omitempty"`
	AgentKey  string `json:"agentKey,omitempty"`
	TeamID    string `json:"teamId,omitempty"`
	Message   string `json:"message"`
}

type SteerResponse struct {
	Accepted bool   `json:"accepted"`
	Status   string `json:"status"`
	RunID    string `json:"runId"`
	SteerID  string `json:"steerId"`
	Detail   string `json:"detail"`
}

type InterruptRequest struct {
	RequestID       string `json:"requestId,omitempty"`
	ChatID          string `json:"chatId,omitempty"`
	RunID           string `json:"runId"`
	AgentKey        string `json:"agentKey,omitempty"`
	TeamID          string `json:"teamId,omitempty"`
	Message         string `json:"message,omitempty"`
	InterruptSource string `json:"source,omitempty"`
	InterruptReason string `json:"reason,omitempty"`
	InterruptDetail string `json:"detail,omitempty"`
}

type InterruptResponse struct {
	Accepted bool   `json:"accepted"`
	Status   string `json:"status"`
	RunID    string `json:"runId"`
	Detail   string `json:"detail"`
}

type CompactRequest struct {
	RequestID string `json:"requestId,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	AgentKey  string `json:"agentKey,omitempty"`
	Trigger   string `json:"trigger,omitempty"`
}

type CompactResponse struct {
	Accepted                   bool           `json:"accepted"`
	Status                     string         `json:"status"`
	RequestID                  string         `json:"requestId,omitempty"`
	ChatID                     string         `json:"chatId,omitempty"`
	CompactID                  string         `json:"compactId,omitempty"`
	SummarySource              string         `json:"summarySource,omitempty"`
	PreCompactEstimatedTokens  int            `json:"preCompactEstimatedTokens,omitempty"`
	PostCompactEstimatedTokens int            `json:"postCompactEstimatedTokens,omitempty"`
	CompressionRatio           float64        `json:"compressionRatio,omitempty"`
	CompactionUsage            map[string]any `json:"compactionUsage,omitempty"`
	Detail                     string         `json:"detail,omitempty"`
}

type DetachRequest struct {
	RunID    string `json:"runId"`
	AgentKey string `json:"agentKey"`
	Reason   string `json:"reason,omitempty"`
}

type DetachResponse struct {
	Accepted        bool   `json:"accepted"`
	Status          string `json:"status"`
	RunID           string `json:"runId"`
	StreamRequestID string `json:"streamRequestId,omitempty"`
	StreamID        string `json:"streamId,omitempty"`
	LastSeq         int64  `json:"lastSeq"`
	Detail          string `json:"detail"`
}

type AccessLevelRequest struct {
	RequestID   string `json:"requestId,omitempty"`
	RunID       string `json:"runId"`
	AgentKey    string `json:"agentKey"`
	AccessLevel string `json:"accessLevel"`
	Reason      string `json:"reason,omitempty"`
}

type AccessLevelResponse struct {
	Accepted            bool   `json:"accepted"`
	Status              string `json:"status"`
	RunID               string `json:"runId"`
	PreviousAccessLevel string `json:"previousAccessLevel,omitempty"`
	AccessLevel         string `json:"accessLevel"`
	Version             int64  `json:"version"`
	Detail              string `json:"detail"`
}

type LearnRequest struct {
	RequestID  string `json:"requestId,omitempty"`
	ChatID     string `json:"chatId"`
	SubjectKey string `json:"subjectKey,omitempty"`
}

type LearnResponse struct {
	Accepted         bool                   `json:"accepted"`
	Status           string                 `json:"status"`
	RequestID        string                 `json:"requestId,omitempty"`
	ChatID           string                 `json:"chatId"`
	ObservationCount int                    `json:"observationCount"`
	Stored           []StoredMemoryResponse `json:"stored,omitempty"`
}

type RememberRequest struct {
	RequestID string `json:"requestId"`
	ChatID    string `json:"chatId"`
}

type RememberResponse struct {
	Accepted      bool                   `json:"accepted"`
	Status        string                 `json:"status"`
	RequestID     string                 `json:"requestId"`
	ChatID        string                 `json:"chatId"`
	MemoryPath    string                 `json:"memoryPath,omitempty"`
	MemoryRoot    string                 `json:"memoryRoot,omitempty"`
	MemoryCount   int                    `json:"memoryCount"`
	Detail        string                 `json:"detail,omitempty"`
	PromptPreview *PromptPreviewResponse `json:"promptPreview,omitempty"`
	Items         []RememberItemResponse `json:"items,omitempty"`
	Stored        []StoredMemoryResponse `json:"stored,omitempty"`
}

type RememberItemResponse struct {
	Summary    string `json:"summary"`
	SubjectKey string `json:"subjectKey,omitempty"`
}

type StoredMemoryResponse struct {
	ID             string   `json:"id"`
	RequestID      string   `json:"requestId,omitempty"`
	ChatID         string   `json:"chatId"`
	AgentKey       string   `json:"agentKey,omitempty"`
	SubjectKey     string   `json:"subjectKey,omitempty"`
	Kind           string   `json:"kind,omitempty"`
	RefID          string   `json:"refId,omitempty"`
	ScopeType      string   `json:"scopeType,omitempty"`
	ScopeKey       string   `json:"scopeKey,omitempty"`
	Title          string   `json:"title,omitempty"`
	Summary        string   `json:"summary"`
	SourceType     string   `json:"sourceType"`
	Category       string   `json:"category"`
	Importance     int      `json:"importance"`
	Confidence     float64  `json:"confidence,omitempty"`
	Status         string   `json:"status,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	CreatedAt      int64    `json:"createdAt"`
	UpdatedAt      int64    `json:"updatedAt"`
	AccessCount    int      `json:"accessCount,omitempty"`
	LastAccessedAt *int64   `json:"lastAccessedAt,omitempty"`
}

type PromptPreviewResponse struct {
	SystemPrompt      string   `json:"systemPrompt,omitempty"`
	UserPrompt        string   `json:"userPrompt,omitempty"`
	ChatName          string   `json:"chatName,omitempty"`
	RawMessageCount   int      `json:"rawMessageCount"`
	EventCount        int      `json:"eventCount"`
	ReferenceCount    int      `json:"referenceCount"`
	RawMessageSamples []string `json:"rawMessageSamples,omitempty"`
	EventSamples      []string `json:"eventSamples,omitempty"`
	ReferenceSamples  []string `json:"referenceSamples,omitempty"`
}

type MemoryUsageItem struct {
	ID        string `json:"id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	ScopeType string `json:"scopeType,omitempty"`
	Title     string `json:"title,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Category  string `json:"category,omitempty"`
}

type MemoryHitItem struct {
	ID        string `json:"id,omitempty"`
	Layer     string `json:"layer,omitempty"`
	Kind      string `json:"kind,omitempty"`
	ScopeType string `json:"scopeType,omitempty"`
	Title     string `json:"title,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Category  string `json:"category,omitempty"`
}

type MemoryUsageSummary struct {
	HasStaticMemory  bool              `json:"hasStaticMemory"`
	StableCount      int               `json:"stableCount"`
	SessionCount     int               `json:"sessionCount"`
	ObservationCount int               `json:"observationCount"`
	StableItems      []MemoryUsageItem `json:"stableItems,omitempty"`
	SessionItems     []MemoryUsageItem `json:"sessionItems,omitempty"`
	ObservationItems []MemoryUsageItem `json:"observationItems,omitempty"`
	UserHint         string            `json:"userHint,omitempty"`
	StableChars      int               `json:"stableChars"`
	SessionChars     int               `json:"sessionChars"`
	ObservationChars int               `json:"observationChars"`
	DisclosedLayers  []string          `json:"disclosedLayers,omitempty"`
	SnapshotID       string            `json:"snapshotId,omitempty"`
	StopReason       string            `json:"stopReason,omitempty"`
	CandidateCounts  map[string]int    `json:"candidateCounts,omitempty"`
	SelectedCounts   map[string]int    `json:"selectedCounts,omitempty"`
}

type AgentSummary struct {
	Key                    string                `json:"key"`
	Name                   string                `json:"name"`
	Icon                   any                   `json:"icon,omitempty"`
	Mode                   string                `json:"mode,omitempty"`
	WorkspaceDir           string                `json:"workspaceDir,omitempty"`
	DefaultModelKey        string                `json:"defaultModelKey,omitempty"`
	DefaultReasoningEffort string                `json:"defaultReasoningEffort,omitempty"`
	Description            string                `json:"-"`
	Role                   string                `json:"role,omitempty"`
	Stats                  AgentChatStats        `json:"stats"`
	Chats                  []ChatSummaryResponse `json:"chats,omitempty"`
	Meta                   map[string]any        `json:"meta,omitempty"`
}

type AgentChatStats struct {
	TotalCount  int `json:"totalCount"`
	UnreadCount int `json:"unreadCount"`
}

type AdminAgentDiagnostic struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	SourcePath string `json:"sourcePath,omitempty"`
}

type AdminAgentSummary struct {
	Key                    string                 `json:"key"`
	Name                   string                 `json:"name"`
	Icon                   any                    `json:"icon,omitempty"`
	Mode                   string                 `json:"mode,omitempty"`
	WorkspaceDir           string                 `json:"workspaceDir,omitempty"`
	DefaultModelKey        string                 `json:"defaultModelKey,omitempty"`
	DefaultReasoningEffort string                 `json:"defaultReasoningEffort,omitempty"`
	Role                   string                 `json:"role,omitempty"`
	Status                 string                 `json:"status"`
	Diagnostics            []AdminAgentDiagnostic `json:"diagnostics,omitempty"`
	Source                 *AgentSource           `json:"source,omitempty"`
	Meta                   map[string]any         `json:"meta,omitempty"`
}

type AgentDetailResponse struct {
	Key          string           `json:"key"`
	Name         string           `json:"name"`
	Icon         any              `json:"icon,omitempty"`
	Description  string           `json:"description,omitempty"`
	Role         string           `json:"role,omitempty"`
	Greetings    []string         `json:"greetings,omitempty"`
	Wonders      []string         `json:"wonders,omitempty"`
	Model        string           `json:"model"`
	Mode         string           `json:"mode"`
	Tools        []string         `json:"tools"`
	Skills       []string         `json:"skills"`
	Controls     []map[string]any `json:"controls"`
	Meta         map[string]any   `json:"meta"`
	Definition   map[string]any   `json:"definition,omitempty"`
	SoulPrompt   string           `json:"soulPrompt"`
	AgentsPrompt string           `json:"agentsPrompt"`
	Source       *AgentSource     `json:"source,omitempty"`
}

type AgentSource struct {
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	AgentDir string `json:"agentDir,omitempty"`
}

type AdminAgentDetailResponse struct {
	Key          string                 `json:"key"`
	Name         string                 `json:"name"`
	Icon         any                    `json:"icon,omitempty"`
	Description  string                 `json:"description,omitempty"`
	Role         string                 `json:"role,omitempty"`
	Model        string                 `json:"model,omitempty"`
	Mode         string                 `json:"mode,omitempty"`
	Tools        []string               `json:"tools"`
	Skills       []string               `json:"skills"`
	Controls     []map[string]any       `json:"controls"`
	Meta         map[string]any         `json:"meta"`
	Definition   map[string]any         `json:"definition,omitempty"`
	SoulPrompt   string                 `json:"soulPrompt,omitempty"`
	AgentsPrompt string                 `json:"agentsPrompt,omitempty"`
	Source       *AgentSource           `json:"source,omitempty"`
	Status       string                 `json:"status"`
	Diagnostics  []AdminAgentDiagnostic `json:"diagnostics,omitempty"`
}

type AdminRegistrySummary struct {
	Category    string                 `json:"category"`
	File        string                 `json:"file"`
	Key         string                 `json:"key,omitempty"`
	Name        string                 `json:"name,omitempty"`
	Status      string                 `json:"status"`
	Diagnostics []AdminAgentDiagnostic `json:"diagnostics,omitempty"`
	Source      *AgentSource           `json:"source,omitempty"`
	Summary     map[string]any         `json:"summary,omitempty"`
	UpdatedAt   int64                  `json:"updatedAt,omitempty"`
	Size        int64                  `json:"size,omitempty"`
}

type AdminRegistryListResponse struct {
	Items []AdminRegistrySummary `json:"items"`
	Total int                    `json:"total"`
}

type AdminRegistryDetailResponse struct {
	AdminRegistrySummary
	Content string         `json:"content"`
	Parsed  map[string]any `json:"parsed,omitempty"`
}

type AdminRegistryDetailRequest struct {
	Category string `json:"category"`
	File     string `json:"file"`
	Content  string `json:"content"`
}

type AdminRegistryValidateRequest struct {
	Category string `json:"category"`
	File     string `json:"file,omitempty"`
	Content  string `json:"content"`
}

type AdminRegistryValidateResponse struct {
	Status      string                 `json:"status"`
	Diagnostics []AdminAgentDiagnostic `json:"diagnostics,omitempty"`
	Summary     map[string]any         `json:"summary,omitempty"`
	Parsed      map[string]any         `json:"parsed,omitempty"`
}

type CreateAgentRequest struct {
	Key          string         `json:"key,omitempty"`
	Definition   map[string]any `json:"definition"`
	SoulPrompt   *string        `json:"soulPrompt,omitempty"`
	AgentsPrompt *string        `json:"agentsPrompt,omitempty"`
}

type UpdateAgentRequest struct {
	Key          string         `json:"key"`
	AgentKey     string         `json:"agentKey,omitempty"`
	Definition   map[string]any `json:"definition"`
	SoulPrompt   *string        `json:"soulPrompt,omitempty"`
	AgentsPrompt *string        `json:"agentsPrompt,omitempty"`
}

type UpdateAgentModelConfigRequest struct {
	Key             string `json:"key,omitempty"`
	AgentKey        string `json:"agentKey,omitempty"`
	ModelKey        string `json:"modelKey"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

type AgentModelConfigResponse struct {
	Key         string         `json:"key"`
	ModelConfig map[string]any `json:"modelConfig"`
}

type DeleteAgentRequest struct {
	Key      string `json:"key"`
	AgentKey string `json:"agentKey,omitempty"`
}

type AgentOrderResponse struct {
	Version   int      `json:"version"`
	Order     []string `json:"order"`
	UpdatedAt int64    `json:"updatedAt"`
}

type UpdateAgentOrderRequest struct {
	Order []string `json:"order"`
}

type OpenAgentWorkspaceRequest struct {
	Key          string `json:"key,omitempty"`
	AgentKey     string `json:"agentKey,omitempty"`
	WorkspaceDir string `json:"workspaceDir,omitempty"`
}

type OpenAgentWorkspaceResponse struct {
	AgentKey     string `json:"agentKey,omitempty"`
	WorkspaceDir string `json:"workspaceDir"`
	Opened       bool   `json:"opened"`
}

type AgentEditorOptionsResponse struct {
	Models            []AgentEditorModelOption     `json:"models"`
	ContextTags       []AgentEditorOption          `json:"contextTags"`
	VisibilityScopes  []AgentEditorOption          `json:"visibilityScopes"`
	Modes             []AgentEditorOption          `json:"modes"`
	ProxyConfigSchema AgentEditorProxyConfigSchema `json:"proxyConfigSchema"`
}

type AgentEditorModelOption struct {
	Key           string `json:"key"`
	Name          string `json:"name,omitempty"`
	Provider      string `json:"provider,omitempty"`
	ModelID       string `json:"modelId,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	IsVision      bool   `json:"isVision"`
	ContextWindow int    `json:"contextWindow,omitempty"`
}

type AgentEditorOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type AgentEditorProxyConfigSchema struct {
	Fields         []AgentEditorProxyConfigField `json:"fields"`
	DefaultTimeout int                           `json:"defaultTimeout"`
}

type AgentEditorProxyConfigField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
}

type CoderModelOptionsResponse struct {
	Models                 []CoderModelOption      `json:"models"`
	ReasoningEfforts       []ReasoningEffortOption `json:"reasoningEfforts"`
	DefaultModelKey        string                  `json:"defaultModelKey,omitempty"`
	DefaultReasoningEffort string                  `json:"defaultReasoningEffort"`
}

type CoderModelOption struct {
	Key           string `json:"key"`
	Name          string `json:"name,omitempty"`
	Provider      string `json:"provider,omitempty"`
	ModelID       string `json:"modelId,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	IsReasoner    bool   `json:"isReasoner"`
	IsVision      bool   `json:"isVision"`
	ContextWindow int    `json:"contextWindow,omitempty"`
}

type ReasoningEffortOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type TeamSummary struct {
	TeamID    string         `json:"teamId"`
	Name      string         `json:"name"`
	Icon      any            `json:"icon,omitempty"`
	AgentKeys []string       `json:"agentKeys"`
	Meta      map[string]any `json:"meta,omitempty"`
}

type SkillSummary struct {
	Key         string         `json:"key"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

type ToolSummary struct {
	Key         string         `json:"key"`
	Name        string         `json:"name"`
	Label       string         `json:"label,omitempty"`
	Description string         `json:"description,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

type ToolDetailResponse struct {
	Key           string         `json:"key"`
	Name          string         `json:"name"`
	Label         string         `json:"label,omitempty"`
	Description   string         `json:"description,omitempty"`
	AfterCallHint string         `json:"afterCallHint,omitempty"`
	Parameters    map[string]any `json:"parameters,omitempty"`
	Meta          map[string]any `json:"meta,omitempty"`
}

type ChatSummaryResponse struct {
	ChatID         string         `json:"chatId"`
	ChatName       string         `json:"chatName"`
	AgentKey       string         `json:"agentKey,omitempty"`
	TeamID         string         `json:"teamId,omitempty"`
	CreatedAt      int64          `json:"createdAt"`
	UpdatedAt      int64          `json:"updatedAt"`
	LastRunID      string         `json:"lastRunId,omitempty"`
	LastRunContent string         `json:"lastRunContent,omitempty"`
	Read           ChatReadState  `json:"read"`
	Awaiting       *Awaiting      `json:"awaiting,omitempty"`
	Usage          *ChatUsageData `json:"usage,omitempty"`
	ActiveRun      *ActiveRunInfo `json:"activeRun,omitempty"`
	Error          *ChatErrorInfo `json:"error,omitempty"`
}

type ChatErrorInfo struct {
	Code    string   `json:"code"`
	Message string   `json:"message"`
	ChatID  string   `json:"chatId,omitempty"`
	RunIDs  []string `json:"runIds,omitempty"`
}

type ChatReadState struct {
	IsRead    bool   `json:"isRead"`
	ReadAt    *int64 `json:"readAt,omitempty"`
	ReadRunID string `json:"readRunId,omitempty"`
}

type Awaiting struct {
	AwaitingID string `json:"awaitingId"`
	RunID      string `json:"runId"`
	Mode       string `json:"mode"`
	Status     string `json:"status"`
	CreatedAt  int64  `json:"createdAt"`
}

type ChatUsageData struct {
	ModelKey                string                  `json:"modelKey,omitempty"`
	PromptTokens            int                     `json:"promptTokens"`
	CompletionTokens        int                     `json:"completionTokens"`
	TotalTokens             int                     `json:"totalTokens"`
	PromptTokensDetails     *PromptTokenDetails     `json:"promptTokensDetails,omitempty"`
	CompletionTokensDetails *CompletionTokenDetails `json:"completionTokensDetails,omitempty"`
	EstimatedCost           *EstimatedCost          `json:"estimatedCost,omitempty"`
	LlmChatCompletionCount  int                     `json:"llmChatCompletionCount,omitempty"`
	ToolCallCount           int                     `json:"toolCallCount,omitempty"`
}

type ChatUsageBreakdown struct {
	LastRun *ChatUsageData `json:"lastRun,omitempty"`
	Chat    *ChatUsageData `json:"chat,omitempty"`
}

type ChatContextWindow struct {
	ModelKey              string `json:"modelKey,omitempty"`
	ReasoningEffort       string `json:"reasoningEffort,omitempty"`
	MaxSize               int    `json:"maxSize,omitempty"`
	CurrentSize           int    `json:"currentSize,omitempty"`
	EstimatedNextCallSize int    `json:"estimatedNextCallSize,omitempty"`
}

type PromptTokenDetails struct {
	CacheHitTokens  int `json:"cacheHitTokens,omitempty"`
	CacheMissTokens int `json:"cacheMissTokens,omitempty"`
}

type CompletionTokenDetails struct {
	ReasoningTokens int `json:"reasoningTokens,omitempty"`
}

type EstimatedCost struct {
	Currency       string  `json:"currency"`
	InputCacheHit  float64 `json:"inputCacheHit"`
	InputCacheMiss float64 `json:"inputCacheMiss"`
	Output         float64 `json:"output"`
	Total          float64 `json:"total"`
}

type MarkChatReadRequest struct {
	ChatID   string `json:"chatId"`
	RunID    string `json:"runId,omitempty"`
	AgentKey string `json:"agentKey,omitempty"`
}

type MarkChatReadResponse struct {
	ChatID           string        `json:"chatId"`
	AgentKey         string        `json:"agentKey,omitempty"`
	LastRunID        string        `json:"lastRunId,omitempty"`
	Read             ChatReadState `json:"read"`
	AgentUnreadCount int           `json:"agentUnreadCount"`
	UpdatedCount     int           `json:"updatedCount"`
}

type ChatDetailResponse struct {
	ChatID         string              `json:"chatId"`
	ChatName       string              `json:"chatName"`
	ResourceTicket string              `json:"resourceTicket,omitempty"`
	RawMessages    []map[string]any    `json:"rawMessages,omitempty"`
	Events         []stream.EventData  `json:"events"`
	Runs           []RunSummary        `json:"runs,omitempty"`
	ActiveRun      *ActiveRunInfo      `json:"activeRun,omitempty"`
	Plan           any                 `json:"plan,omitempty"`
	Planning       any                 `json:"planning,omitempty"`
	Artifact       any                 `json:"artifact,omitempty"`
	References     []Reference         `json:"references,omitempty"`
	Usage          *ChatUsageBreakdown `json:"usage,omitempty"`
	ContextWindow  *ChatContextWindow  `json:"contextWindow,omitempty"`
}

type ArchivedChatDetailResponse struct {
	ChatID         string             `json:"chatId"`
	ChatName       string             `json:"chatName"`
	ResourceTicket string             `json:"resourceTicket,omitempty"`
	RawMessages    []map[string]any   `json:"rawMessages,omitempty"`
	Events         []stream.EventData `json:"events"`
	Runs           []RunSummary       `json:"runs,omitempty"`
	ActiveRun      *ActiveRunInfo     `json:"activeRun,omitempty"`
	Plan           any                `json:"plan,omitempty"`
	Planning       any                `json:"planning,omitempty"`
	Artifact       any                `json:"artifact,omitempty"`
	References     []Reference        `json:"references,omitempty"`
	Usage          *ChatUsageData     `json:"usage,omitempty"`
}

type RunSummary struct {
	RunID           string        `json:"runId"`
	ChatID          string        `json:"chatId"`
	AgentKey        string        `json:"agentKey,omitempty"`
	InitialMessage  string        `json:"initialMessage,omitempty"`
	AssistantText   string        `json:"assistantText,omitempty"`
	FinishReason    string        `json:"finishReason,omitempty"`
	StartedAt       int64         `json:"startedAt"`
	CompletedAt     int64         `json:"completedAt"`
	Usage           ChatUsageData `json:"usage"`
	FeedbackType    string        `json:"feedbackType,omitempty"`
	FeedbackComment string        `json:"feedbackComment,omitempty"`
	FeedbackAt      int64         `json:"feedbackAt,omitempty"`
}

type ActiveRunInfo struct {
	RunID        string `json:"runId"`
	State        string `json:"state"`
	LastSeq      int64  `json:"lastSeq"`
	OldestSeq    int64  `json:"oldestSeq"`
	StartedAt    int64  `json:"startedAt"`
	PlanningMode bool   `json:"planningMode,omitempty"`
}

type SessionSearchRequest struct {
	ChatID string `json:"chatId"`
	Query  string `json:"query"`
	Limit  int    `json:"limit,omitempty"`
}

type SessionSearchResult struct {
	Kind      string         `json:"kind"`
	ChatID    string         `json:"chatId"`
	RunID     string         `json:"runId,omitempty"`
	Stage     string         `json:"stage,omitempty"`
	Role      string         `json:"role,omitempty"`
	Timestamp int64          `json:"timestamp"`
	Snippet   string         `json:"snippet"`
	Score     int            `json:"score"`
	Meta      map[string]any `json:"meta,omitempty"`
}

type SessionSearchResponse struct {
	ChatID  string                `json:"chatId"`
	Query   string                `json:"query"`
	Count   int                   `json:"count"`
	Results []SessionSearchResult `json:"results"`
}

type FeedbackRequest struct {
	ChatID  string `json:"chatId"`
	RunID   string `json:"runId"`
	Type    string `json:"type"`
	Comment string `json:"comment,omitempty"`
}

type FeedbackResponse struct {
	ChatID string `json:"chatId"`
	RunID  string `json:"runId"`
	Type   string `json:"type"`
	SetAt  int64  `json:"setAt"`
}

type DeleteChatRequest struct {
	ChatID string `json:"chatId"`
}

type DeleteChatResponse struct {
	ChatID  string `json:"chatId"`
	Deleted bool   `json:"deleted"`
}

type RenameChatRequest struct {
	ChatID   string `json:"chatId,omitempty"`
	ChatName string `json:"chatName"`
}

type RenameChatResponse struct {
	ChatID   string `json:"chatId"`
	ChatName string `json:"chatName"`
	Updated  bool   `json:"updated"`
}
