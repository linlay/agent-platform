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

func Failure(code int, msg string, errors ...map[string]any) ApiResponse[map[string]any] {
	data := map[string]any{}
	if len(errors) > 0 && len(errors[0]) > 0 {
		data["error"] = cloneAnyMap(errors[0])
	}
	return ApiResponse[map[string]any]{
		Code: code,
		Msg:  msg,
		Data: data,
	}
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

const (
	QueryRoleUser       = "user"
	QueryRoleAssistant  = "assistant"
	QueryRoleAutomation = "automation"
	QueryRoleSystem     = "system"

	QueryRoleValidationMessage = "role must be user, assistant, automation, or system"

	ChatSourceQuery            = "query"
	ChatSourceQueryPrefix      = "query:"
	ChatSourceAutomationPrefix = "automation:"
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
	RequestID string `json:"requestId,omitempty"`
	RunID     string `json:"runId,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	AgentKey  string `json:"agentKey,omitempty"`
	TeamID    string `json:"teamId,omitempty"`
	Role      string `json:"role,omitempty"`
	Message   string `json:"message"`
	// Trusted channel hint for the remote actor. Ignored outside gateway
	// contexts when deriving chat summary source.
	SourceUser      string             `json:"sourceUser,omitempty"`
	References      []Reference        `json:"references,omitempty"`
	Params          map[string]any     `json:"params,omitempty"`
	Scene           *Scene             `json:"scene,omitempty"`
	Stream          *bool              `json:"stream,omitempty"`
	IncludeUsage    bool               `json:"includeUsage,omitempty"`
	IncludeFullText bool               `json:"includeFullText,omitempty"`
	PlanningMode    *bool              `json:"planningMode,omitempty"`
	AccessLevel     string             `json:"accessLevel,omitempty"`
	Model           *QueryModelOptions `json:"model,omitempty"`

	// Internal runtime hint: the stream bootstrap already emitted the synthetic
	// request.query for this run, so agent mode prefixes must not emit it again.
	SyntheticQueryBootstrapped bool `json:"-"`

	// Internal runtime hint for the chat creation source. External JSON input
	// cannot set this field.
	ChatSource string `json:"-"`
}

type QueryResponse struct {
	Content  string         `json:"content"`
	FullText *string        `json:"fullText,omitempty"`
	Usage    *ChatUsageData `json:"usage,omitempty"`
}

type QueryModelOptions struct {
	Key             string `json:"key,omitempty"`
	ModelID         string `json:"modelId,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	ServiceTier     string `json:"serviceTier,omitempty"`
}

type Scene struct {
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
}

type Reference struct {
	ID        string         `json:"id,omitempty"`
	Type      string         `json:"type,omitempty"`
	Name      string         `json:"name,omitempty"`
	Path      string         `json:"path,omitempty"`
	MimeType  string         `json:"mimeType,omitempty"`
	SizeBytes *int64         `json:"sizeBytes,omitempty"`
	URL       string         `json:"url,omitempty"`
	SHA256    string         `json:"sha256,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

const ReferenceSandboxPathRemovedMessage = "reference sandboxPath has been removed; use path"

func (r *Reference) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if _, ok := raw["sandboxPath"]; ok {
		return fmt.Errorf(ReferenceSandboxPathRemovedMessage)
	}
	type referenceAlias Reference
	var decoded referenceAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = Reference(decoded)
	return nil
}

type SubmitRequest struct {
	ChatID            string       `json:"chatId,omitempty"`
	RunID             string       `json:"runId"`
	AgentKey          string       `json:"agentKey"`
	AwaitingID        string       `json:"awaitingId"`
	SubmitID          string       `json:"submitId,omitempty"`
	Locale            string       `json:"locale,omitempty"`
	Params            SubmitParams `json:"params"`
	ContinuationRunID string       `json:"-"`
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
	Level     string `json:"level,omitempty"`
}

type CompactResponse struct {
	Accepted                   bool           `json:"accepted"`
	Status                     string         `json:"status"`
	RequestID                  string         `json:"requestId,omitempty"`
	ChatID                     string         `json:"chatId,omitempty"`
	CompactID                  string         `json:"compactId,omitempty"`
	Level                      string         `json:"level,omitempty"`
	SummarySource              string         `json:"summarySource,omitempty"`
	PreCompactEstimatedTokens  int            `json:"preCompactEstimatedTokens,omitempty"`
	PostCompactEstimatedTokens int            `json:"postCompactEstimatedTokens,omitempty"`
	CompressionRatio           float64        `json:"compressionRatio,omitempty"`
	CompactionUsage            map[string]any `json:"compactionUsage,omitempty"`
	ToolsCleared               int            `json:"toolsCleared,omitempty"`
	ToolsKept                  int            `json:"toolsKept,omitempty"`
	TokensFreed                int            `json:"tokensFreed,omitempty"`
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
	Key                    string                     `json:"key"`
	Name                   string                     `json:"name"`
	Icon                   any                        `json:"icon,omitempty"`
	Mode                   string                     `json:"mode,omitempty"`
	WorkspaceDir           string                     `json:"workspaceDir,omitempty"`
	DefaultModelKey        string                     `json:"defaultModelKey,omitempty"`
	DefaultReasoningEffort string                     `json:"defaultReasoningEffort,omitempty"`
	ModelConfig            map[string]any             `json:"modelConfig,omitempty"`
	ModelOptions           *CoderModelOptionsResponse `json:"modelOptions,omitempty"`
	Description            string                     `json:"-"`
	Role                   string                     `json:"role,omitempty"`
	Stats                  AgentChatStats             `json:"stats"`
	Chats                  []ChatSummaryResponse      `json:"chats,omitempty"`
	Meta                   map[string]any             `json:"meta,omitempty"`
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
	Key          string                     `json:"key"`
	Name         string                     `json:"name"`
	Icon         any                        `json:"icon,omitempty"`
	Description  string                     `json:"description,omitempty"`
	Role         string                     `json:"role,omitempty"`
	Greetings    []string                   `json:"greetings,omitempty"`
	Wonders      []string                   `json:"wonders,omitempty"`
	Model        string                     `json:"model,omitempty"`
	Mode         string                     `json:"mode"`
	Tools        []string                   `json:"tools"`
	Skills       []string                   `json:"skills"`
	Controls     []map[string]any           `json:"controls"`
	Meta         map[string]any             `json:"meta"`
	ModelConfig  map[string]any             `json:"modelConfig,omitempty"`
	ModelOptions *CoderModelOptionsResponse `json:"modelOptions,omitempty"`
	Definition   map[string]any             `json:"definition,omitempty"`
	SoulPrompt   string                     `json:"soulPrompt,omitempty"`
	AgentsPrompt string                     `json:"agentsPrompt,omitempty"`
	Source       *AgentSource               `json:"source,omitempty"`
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

type AdminRegistryListDiagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type AdminRegistryListItem struct {
	Category        string                       `json:"category"`
	File            string                       `json:"file"`
	Key             string                       `json:"key,omitempty"`
	Name            string                       `json:"name,omitempty"`
	Status          string                       `json:"status"`
	Summary         map[string]any               `json:"summary,omitempty"`
	Diagnostic      *AdminRegistryListDiagnostic `json:"diagnostic,omitempty"`
	DiagnosticCount int                          `json:"diagnosticCount,omitempty"`
	UpdatedAt       int64                        `json:"updatedAt,omitempty"`
}

type AdminRegistryListResponse struct {
	Items []AdminRegistryListItem `json:"items"`
	Total int                     `json:"total"`
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

type AdminChannelListResponse struct {
	Items []AdminChannelSummary `json:"items"`
	Total int                   `json:"total"`
}

type AdminChannelSummary struct {
	ID         string                        `json:"id"`
	Name       string                        `json:"name,omitempty"`
	Type       string                        `json:"type,omitempty"`
	Mode       string                        `json:"mode,omitempty"`
	Transport  string                        `json:"transport,omitempty"`
	Protocol   string                        `json:"protocol,omitempty"`
	Status     string                        `json:"status"`
	Connection AdminChannelConnectionSummary `json:"connection"`
	Agents     AdminChannelAgentSummary      `json:"agents"`
	Config     AdminChannelConfigSummary     `json:"config"`
}

type AdminChannelConnectionSummary struct {
	Connected       bool   `json:"connected"`
	ActiveCount     int    `json:"activeCount"`
	LatestSessionID string `json:"latestSessionId,omitempty"`
	ConnectedAt     int64  `json:"connectedAt,omitempty"`
	LastSeenAt      int64  `json:"lastSeenAt,omitempty"`
}

type AdminChannelAgentSummary struct {
	AllowedAllAgents bool                      `json:"allowedAllAgents"`
	AllowedCount     int                       `json:"allowedCount"`
	AllowedAgentKeys []string                  `json:"allowedAgentKeys,omitempty"`
	ImportCount      int                       `json:"importCount"`
	ExportCount      int                       `json:"exportCount"`
	Imports          []AdminChannelAgentImport `json:"imports,omitempty"`
	Exports          []AdminChannelAgentExport `json:"exports,omitempty"`
}

type AdminChannelAgentImport struct {
	AgentKey       string `json:"agentKey"`
	Name           string `json:"name,omitempty"`
	RemoteAgentKey string `json:"remoteAgentKey,omitempty"`
}

type AdminChannelAgentExport struct {
	AgentKey         string                 `json:"agentKey"`
	Name             string                 `json:"name,omitempty"`
	ExternalAgentKey string                 `json:"externalAgentKey,omitempty"`
	Allow            AdminChannelAllowFlags `json:"allow"`
}

type AdminChannelAllowFlags struct {
	Query        bool `json:"query"`
	Submit       bool `json:"submit"`
	Steer        bool `json:"steer"`
	Interrupt    bool `json:"interrupt"`
	FileTransfer bool `json:"fileTransfer"`
}

type AdminChannelConfigSummary struct {
	EndpointURL                      string `json:"endpointUrl,omitempty"`
	EndpointPath                     string `json:"endpointPath,omitempty"`
	AuthType                         string `json:"authType,omitempty"`
	HeartbeatIntervalSeconds         int64  `json:"heartbeatIntervalSeconds,omitempty"`
	ReconnectHandshakeTimeoutSeconds int64  `json:"reconnectHandshakeTimeoutSeconds,omitempty"`
	ReconnectMinSeconds              int64  `json:"reconnectMinSeconds,omitempty"`
	ReconnectMaxSeconds              int64  `json:"reconnectMaxSeconds,omitempty"`
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
	ServiceTier     string `json:"serviceTier,omitempty"`
}

type AgentModelConfigResponse struct {
	Key         string         `json:"key"`
	ModelConfig map[string]any `json:"modelConfig"`
}

type DeleteAgentRequest struct {
	Key      string `json:"key"`
	AgentKey string `json:"agentKey,omitempty"`
}

type UpdateAgentNameRequest struct {
	Key      string `json:"key"`
	AgentKey string `json:"agentKey,omitempty"`
	Name     string `json:"name"`
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
	Models              []AgentEditorModelOption       `json:"models"`
	ContextTags         []AgentEditorOption            `json:"contextTags"`
	VisibilityScopes    []AgentEditorOption            `json:"visibilityScopes"`
	Modes               []AgentEditorOption            `json:"modes"`
	ProxyConfigSchema   AgentEditorProxyConfigSchema   `json:"proxyConfigSchema"`
	ChannelConfigSchema AgentEditorChannelConfigSchema `json:"channelConfigSchema"`
}

type AgentEditorModelOption struct {
	Key           string `json:"key"`
	Name          string `json:"name,omitempty"`
	Provider      string `json:"provider,omitempty"`
	ModelID       string `json:"modelId,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	IsVision      bool   `json:"isVision"`
	ContextWindow int    `json:"contextWindow,omitempty"`
	Timeout       int    `json:"timeout,omitempty"`
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

type AgentEditorChannelConfigSchema struct {
	ImportFields []AgentEditorProxyConfigField `json:"importFields"`
	ExportFields []AgentEditorProxyConfigField `json:"exportFields"`
	AllowFields  []AgentEditorProxyConfigField `json:"allowFields"`
}

type CoderModelOptionsResponse struct {
	Models                 []CoderModelOption      `json:"models"`
	ReasoningEfforts       []ReasoningEffortOption `json:"reasoningEfforts"`
	ServiceTiers           []ServiceTierOption     `json:"serviceTiers,omitempty"`
	DefaultModelKey        string                  `json:"defaultModelKey,omitempty"`
	DefaultReasoningEffort string                  `json:"defaultReasoningEffort"`
	DefaultServiceTier     string                  `json:"defaultServiceTier,omitempty"`
}

type CoderModelOption struct {
	Key              string   `json:"key"`
	Name             string   `json:"name,omitempty"`
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

type ReasoningEffortOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type ServiceTierOption struct {
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

type AdminSkillSummary struct {
	Key             string                       `json:"key"`
	Name            string                       `json:"name"`
	Description     string                       `json:"description,omitempty"`
	Meta            map[string]any               `json:"meta,omitempty"`
	Status          string                       `json:"status"`
	Diagnostic      *AdminRegistryListDiagnostic `json:"diagnostic,omitempty"`
	DiagnosticCount int                          `json:"diagnosticCount,omitempty"`
	UpdatedAt       int64                        `json:"updatedAt,omitempty"`
	Size            int64                        `json:"size,omitempty"`
	UsedByAgents    []string                     `json:"usedByAgents,omitempty"`
}

type AdminSkillFile struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Size      int64  `json:"size,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	MimeType  string `json:"mimeType,omitempty"`
	Text      bool   `json:"text"`
	Binary    bool   `json:"binary"`
	SHA256    string `json:"sha256,omitempty"`
}

type AdminSkillDetailResponse struct {
	AdminSkillSummary
	Source      *AgentSource           `json:"source,omitempty"`
	Diagnostics []AdminAgentDiagnostic `json:"diagnostics,omitempty"`
	SkillMd     string                 `json:"skillMd,omitempty"`
	Files       []AdminSkillFile       `json:"files,omitempty"`
}

type AdminSkillInlineFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
}

type CreateAdminSkillRequest struct {
	Key     string                 `json:"key"`
	SkillMd string                 `json:"skillMd"`
	Files   []AdminSkillInlineFile `json:"files,omitempty"`
}

type DeleteAdminSkillRequest struct {
	Key string `json:"key"`
}

type DeleteAdminSkillResponse struct {
	Key          string   `json:"key"`
	Deleted      bool     `json:"deleted"`
	UsedByAgents []string `json:"usedByAgents,omitempty"`
}

type AdminSkillFileResponse struct {
	Key       string `json:"key"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Encoding  string `json:"encoding"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
}

type WriteAdminSkillFileRequest struct {
	Key        string `json:"key"`
	Path       string `json:"path"`
	Content    string `json:"content"`
	Encoding   string `json:"encoding,omitempty"`
	BaseSHA256 string `json:"baseSha256,omitempty"`
}

type DeleteAdminSkillFileRequest struct {
	Key        string `json:"key"`
	Path       string `json:"path"`
	Recursive  bool   `json:"recursive,omitempty"`
	BaseSHA256 string `json:"baseSha256,omitempty"`
}

type MkdirAdminSkillFileRequest struct {
	Key  string `json:"key"`
	Path string `json:"path"`
}

type RenameAdminSkillFileRequest struct {
	Key       string `json:"key"`
	FromPath  string `json:"fromPath"`
	ToPath    string `json:"toPath"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

type AdminSkillFileMutationResponse struct {
	Key          string          `json:"key"`
	Path         string          `json:"path,omitempty"`
	FromPath     string          `json:"fromPath,omitempty"`
	ToPath       string          `json:"toPath,omitempty"`
	Created      bool            `json:"created,omitempty"`
	Updated      bool            `json:"updated,omitempty"`
	Deleted      bool            `json:"deleted,omitempty"`
	Renamed      bool            `json:"renamed,omitempty"`
	File         *AdminSkillFile `json:"file,omitempty"`
	Reloaded     bool            `json:"reloaded,omitempty"`
	UsedByAgents []string        `json:"usedByAgents,omitempty"`
}

type AdminSkillV2Summary struct {
	Key             string                       `json:"key"`
	Name            string                       `json:"name"`
	Description     string                       `json:"description,omitempty"`
	Meta            map[string]any               `json:"meta,omitempty"`
	Status          string                       `json:"status"`
	Diagnostic      *AdminRegistryListDiagnostic `json:"diagnostic,omitempty"`
	DiagnosticCount int                          `json:"diagnosticCount,omitempty"`
	UpdatedAt       int64                        `json:"updatedAt,omitempty"`
	Size            int64                        `json:"size,omitempty"`
	UsedByAgents    []string                     `json:"usedByAgents,omitempty"`
	Source          *AgentSource                 `json:"source,omitempty"`
}

type AdminSkillV2Capabilities struct {
	MaxTextBytes   int64 `json:"maxTextBytes"`
	MaxUploadBytes int64 `json:"maxUploadBytes"`
	CanCreate      bool  `json:"canCreate"`
	CanRename      bool  `json:"canRename"`
	CanDelete      bool  `json:"canDelete"`
	CanUpload      bool  `json:"canUpload"`
	CanDownload    bool  `json:"canDownload"`
}

type AdminSkillV2FileManifest struct {
	Revision        string                  `json:"revision"`
	DefaultOpenPath string                  `json:"defaultOpenPath,omitempty"`
	Counts          AdminSkillV2FileCounts  `json:"counts"`
	Entries         []AdminSkillV2FileEntry `json:"entries"`
}

type AdminSkillV2FileCounts struct {
	Files       int   `json:"files"`
	Directories int   `json:"directories"`
	TextFiles   int   `json:"textFiles"`
	BinaryFiles int   `json:"binaryFiles"`
	TotalSize   int64 `json:"totalSize"`
}

type AdminSkillV2FileEntry struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	ParentPath   string `json:"parentPath"`
	Depth        int    `json:"depth"`
	Order        int    `json:"order"`
	Size         int64  `json:"size,omitempty"`
	UpdatedAt    int64  `json:"updatedAt,omitempty"`
	MimeType     string `json:"mimeType,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	ContentKind  string `json:"contentKind"`
	Language     string `json:"language,omitempty"`
	Role         string `json:"role,omitempty"`
	Editable     bool   `json:"editable"`
	Downloadable bool   `json:"downloadable"`
	Uploadable   bool   `json:"uploadable"`
	Renamable    bool   `json:"renamable"`
	Deletable    bool   `json:"deletable"`
}

type AdminSkillV2TextFile struct {
	Key       string `json:"key"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Encoding  string `json:"encoding"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	Editable  bool   `json:"editable"`
}

type AdminSkillV2DetailResponse struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Skill         AdminSkillV2Summary      `json:"skill"`
	Capabilities  AdminSkillV2Capabilities `json:"capabilities"`
	FileManifest  AdminSkillV2FileManifest `json:"fileManifest"`
	Diagnostics   []AdminAgentDiagnostic   `json:"diagnostics,omitempty"`
	OpenedFile    *AdminSkillV2TextFile    `json:"openedFile,omitempty"`
}

type CreateAdminSkillV2FileRequest struct {
	Key      string `json:"key"`
	Path     string `json:"path"`
	Content  string `json:"content,omitempty"`
	Encoding string `json:"encoding,omitempty"`
}

type AdminSkillV2MutationResponse struct {
	Key          string                    `json:"key"`
	Action       string                    `json:"action"`
	SelectedPath string                    `json:"selectedPath,omitempty"`
	Entry        *AdminSkillV2FileEntry    `json:"entry,omitempty"`
	OpenedFile   *AdminSkillV2TextFile     `json:"openedFile,omitempty"`
	FileManifest *AdminSkillV2FileManifest `json:"fileManifest,omitempty"`
	Skill        *AdminSkillV2Summary      `json:"skill,omitempty"`
	Diagnostics  []AdminAgentDiagnostic    `json:"diagnostics,omitempty"`
	Reloaded     bool                      `json:"reloaded"`
}

type ValidateAdminSkillV2Request struct {
	Key string `json:"key"`
}

type AdminSkillV2ValidateResponse struct {
	Key         string                 `json:"key"`
	Status      string                 `json:"status"`
	Diagnostics []AdminAgentDiagnostic `json:"diagnostics,omitempty"`
	UpdatedAt   int64                  `json:"updatedAt,omitempty"`
	Size        int64                  `json:"size,omitempty"`
}

type ToolSummary struct {
	Key            string `json:"key"`
	Name           string `json:"name"`
	Label          string `json:"label,omitempty"`
	Description    string `json:"description,omitempty"`
	Kind           string `json:"kind"`
	SourceType     string `json:"sourceType"`
	SourceCategory string `json:"sourceCategory"`
	ServerKey      string `json:"serverKey,omitempty"`
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
	Source         string         `json:"source,omitempty"`
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
	Code     string    `json:"code"`
	Message  string    `json:"message"`
	ChatID   string    `json:"chatId,omitempty"`
	RunIDs   []string  `json:"runIds,omitempty"`
	Awaiting *Awaiting `json:"awaiting,omitempty"`
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
	Timing                  *ChatUsageTiming        `json:"timing,omitempty"`
}

type ChatUsageBreakdown struct {
	LastRun *ChatUsageData `json:"lastRun,omitempty"`
	Chat    *ChatUsageData `json:"chat,omitempty"`
}

type ChatContextWindow struct {
	MaxSize               int    `json:"maxSize,omitempty"`
	CurrentSize           int    `json:"currentSize,omitempty"`
	EstimatedNextCallSize int    `json:"estimatedNextCallSize,omitempty"`
	ModelKey              string `json:"modelKey,omitempty"`
	ReasoningEffort       string `json:"reasoningEffort,omitempty"`
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

type ChatUsageTiming struct {
	FirstTokenLatencyMs      int64 `json:"firstTokenLatencyMs,omitempty"`
	FirstTokenLatencyTotalMs int64 `json:"firstTokenLatencyTotalMs,omitempty"`
	FirstTokenLatencyCount   int   `json:"firstTokenLatencyCount,omitempty"`
	GenerationDurationMs     int64 `json:"generationDurationMs,omitempty"`
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
	Source         string              `json:"source,omitempty"`
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
	CreatedAt      int64              `json:"createdAt"`
	LastRunAt      int64              `json:"lastRunAt"`
	ArchivedAt     int64              `json:"archivedAt"`
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

type DeriveChatRequest struct {
	SourceChatID string `json:"sourceChatId"`
	SourceRunID  string `json:"sourceRunId,omitempty"`
	ChatID       string `json:"chatId,omitempty"`
	ChatName     string `json:"chatName,omitempty"`
}

type DeriveChatResponse struct {
	ChatID       string `json:"chatId"`
	ChatName     string `json:"chatName"`
	AgentKey     string `json:"agentKey,omitempty"`
	TeamID       string `json:"teamId,omitempty"`
	Source       string `json:"source,omitempty"`
	SourceChatID string `json:"sourceChatId"`
	SourceRunID  string `json:"sourceRunId"`
	LastRunID    string `json:"lastRunId"`
	CopiedRuns   int    `json:"copiedRuns"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
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
