package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/skillmeta"
	"agent-platform/internal/stream"

	"agent-platform/internal/interaction"
)

type ApiResponse[T any] struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data T      `json:"data"`
}

type FileHistoryResponse struct {
	Content string `json:"content"`
}

type AgentFileResponse struct {
	AgentKey       string `json:"agentKey"`
	WorkspaceRoot  string `json:"workspaceRoot"`
	RequestedPath  string `json:"requestedPath"`
	Path           string `json:"path"`
	AbsolutePath   string `json:"absolutePath"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	ContentKind    string `json:"contentKind"`
	DocumentKind   string `json:"documentKind"`
	Revision       string `json:"revision"`
	MimeType       string `json:"mimeType,omitempty"`
	Encoding       string `json:"encoding,omitempty"`
	Content        string `json:"content,omitempty"`
	SizeBytes      int64  `json:"sizeBytes"`
	ReadBytes      int    `json:"readBytes,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	ModifiedUnixMs int64  `json:"modifiedUnixMs,omitempty"`
	Truncated      bool   `json:"truncated"`
	ContentURL     string `json:"contentUrl,omitempty"`
}

type DocumentSource struct {
	Kind         string `json:"kind"`
	AgentKey     string `json:"agentKey"`
	Path         string `json:"path,omitempty"`
	ChatID       string `json:"chatId,omitempty"`
	ResourceID   string `json:"resourceId,omitempty"`
	RelativePath string `json:"relativePath,omitempty"`
}

type DocumentCommitPayload struct {
	Kind       string  `json:"kind"`
	MIMEType   string  `json:"mimeType,omitempty"`
	Encoding   string  `json:"encoding,omitempty"`
	Text       *string `json:"text,omitempty"`
	DataBase64 string  `json:"dataBase64,omitempty"`
}

type DocumentCommitRequest struct {
	Operation        string                `json:"operation"`
	Source           DocumentSource        `json:"source"`
	Mode             string                `json:"mode"`
	ExpectedRevision string                `json:"expectedRevision"`
	Payload          DocumentCommitPayload `json:"payload"`
}

type DocumentCommitResponse struct {
	SourceKind   string `json:"sourceKind"`
	AgentKey     string `json:"agentKey"`
	Path         string `json:"path,omitempty"`
	ChatID       string `json:"chatId,omitempty"`
	ArtifactID   string `json:"artifactId,omitempty"`
	ResourceID   string `json:"resourceId,omitempty"`
	RelativePath string `json:"relativePath,omitempty"`
	Revision     string `json:"revision"`
	DocumentKind string `json:"documentKind"`
	MIMEType     string `json:"mimeType,omitempty"`
}

type ProjectTreeEntry struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Kind           string `json:"kind"`
	TargetKind     string `json:"targetKind,omitempty"`
	Accessible     bool   `json:"accessible"`
	SizeBytes      int64  `json:"sizeBytes,omitempty"`
	ModifiedUnixMs int64  `json:"modifiedUnixMs,omitempty"`
}

// ProjectGitResponse is a live, read-only snapshot, never Agent configuration.
type ProjectGitResponse struct {
	AgentKey string `json:"agentKey"`
	Status   string `json:"status"`
	Branch   string `json:"branch,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Revision string `json:"revision,omitempty"`
}

type ProjectGitBranchesResponse struct {
	Git            ProjectGitResponse `json:"git"`
	Branches       []string           `json:"branches"`
	CanChange      bool               `json:"canChange"`
	BlockedReason  string             `json:"blockedReason,omitempty"`
	ExpectedBranch string             `json:"expectedBranch,omitempty"`
}

type ProjectGitBranchRequest struct {
	AgentKey         string `json:"agentKey"`
	Operation        string `json:"operation"`
	Branch           string `json:"branch"`
	ExpectedRevision string `json:"expectedRevision"`
}

type ProjectTreeResponse struct {
	AgentKey      string             `json:"agentKey"`
	Mode          string             `json:"mode"`
	WorkspaceName string             `json:"workspaceName"`
	Path          string             `json:"path"`
	Revision      string             `json:"revision"`
	Entries       []ProjectTreeEntry `json:"entries"`
	NextCursor    string             `json:"nextCursor,omitempty"`
}

type ProjectHistoryVersion struct {
	Exists    bool   `json:"exists"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
}

type ProjectChangeItem struct {
	RunID      string                `json:"runId"`
	Path       string                `json:"path"`
	ChangeType string                `json:"changeType"`
	UpdatedAt  int64                 `json:"updatedAt,omitempty"`
	Original   ProjectHistoryVersion `json:"original"`
	Current    ProjectHistoryVersion `json:"current"`
}

type ProjectChangeRun struct {
	RunID     string `json:"runId"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	FileCount int    `json:"fileCount"`
}

type ProjectChangesResponse struct {
	AgentKey   string              `json:"agentKey"`
	ChatID     string              `json:"chatId"`
	Revision   string              `json:"revision"`
	Runs       []ProjectChangeRun  `json:"runs"`
	Items      []ProjectChangeItem `json:"items"`
	NextCursor string              `json:"nextCursor,omitempty"`
}

type ProjectDiffVersion struct {
	Exists    bool   `json:"exists"`
	Content   string `json:"content,omitempty"`
	Encoding  string `json:"encoding,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
}

type ProjectDiffResponse struct {
	AgentKey   string             `json:"agentKey"`
	ChatID     string             `json:"chatId"`
	RunID      string             `json:"runId"`
	Path       string             `json:"path"`
	ChangeType string             `json:"changeType"`
	Original   ProjectDiffVersion `json:"original"`
	Current    ProjectDiffVersion `json:"current"`
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
	QueryRoleUser              = queryinput.QueryRoleUser
	QueryRoleAssistant         = queryinput.QueryRoleAssistant
	QueryRoleAutomation        = queryinput.QueryRoleAutomation
	QueryRoleSystem            = queryinput.QueryRoleSystem
	QueryRoleValidationMessage = queryinput.QueryRoleValidationMessage
	ChatSourceQuery            = queryinput.ChatSourceQuery
	ChatSourceQueryPrefix      = queryinput.ChatSourceQueryPrefix
	ChatSourceAutomationPrefix = queryinput.ChatSourceAutomationPrefix
	ChatSourceRunQueryPrefix   = queryinput.ChatSourceRunQueryPrefix
)

func NormalizeQueryRole(role string) (string, bool) { return queryinput.NormalizeQueryRole(role) }

func QueryRoleVisible(role string) bool { return queryinput.QueryRoleVisible(role) }

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
	// HTTP query routing only. WebSocket execution is selected by the authenticated connection.
	Lane      string `json:"lane,omitempty"`
	BTWID     string `json:"btwId,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	RunID     string `json:"runId,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	AgentKey  string `json:"agentKey,omitempty"`
	TeamID    string `json:"teamId,omitempty"`
	Role      string `json:"role,omitempty"`
	Hidden    *bool  `json:"hidden,omitempty"`
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
	EditingMode     *bool              `json:"editingMode,omitempty"`
	MustUseSkills   []string           `json:"mustUseSkills,omitempty"`
	AccessLevel     string             `json:"accessLevel,omitempty"`
	Model           *QueryModelOptions `json:"model,omitempty"`

	// Internal runtime hint: the stream bootstrap already emitted the synthetic
	// request.query for this run, so agent mode prefixes must not emit it again.
	SyntheticQueryBootstrapped bool `json:"-"`

	// Internal runtime hint for the chat creation source. External JSON input
	// cannot set this field.
	ChatSource string `json:"-"`

	// TrustedQueryMetadata is merged into the persisted request.query event.
	// External JSON input cannot set this field.
	TrustedQueryMetadata map[string]any `json:"-"`
}

const RequiredSkillKeysRemovedMessage = "requiredSkillKeys has been removed; use mustUseSkills"

var ErrRequiredSkillKeysRemoved = errors.New(RequiredSkillKeysRemovedMessage)

func (r *QueryRequest) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, exists := fields["requiredSkillKeys"]; exists {
		return ErrRequiredSkillKeysRemoved
	}
	type queryRequestAlias QueryRequest
	var decoded queryRequestAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = QueryRequest(decoded)
	return nil
}

type QueryResponse struct {
	Content  string         `json:"content"`
	FullText *string        `json:"fullText,omitempty"`
	Usage    *ChatUsageData `json:"usage,omitempty"`
}

type BTWRequest struct {
	RequestID       string             `json:"requestId,omitempty"`
	RunID           string             `json:"runId,omitempty"`
	ChatID          string             `json:"chatId"`
	BTWID           string             `json:"btwId,omitempty"`
	Message         string             `json:"message"`
	References      []Reference        `json:"references,omitempty"`
	Params          map[string]any     `json:"params,omitempty"`
	Scene           *Scene             `json:"scene,omitempty"`
	Stream          *bool              `json:"stream,omitempty"`
	IncludeUsage    bool               `json:"includeUsage,omitempty"`
	IncludeFullText bool               `json:"includeFullText,omitempty"`
	AccessLevel     string             `json:"accessLevel,omitempty"`
	Model           *QueryModelOptions `json:"model,omitempty"`
}

type BTWResponse struct {
	BTWID        string         `json:"btwId"`
	ParentChatID string         `json:"parentChatId"`
	RunID        string         `json:"runId"`
	Content      string         `json:"content"`
	FullText     *string        `json:"fullText,omitempty"`
	Usage        *ChatUsageData `json:"usage,omitempty"`
}

type QueryModelOptions = queryinput.QueryModelOptions

type Scene = queryinput.Scene

type Reference = queryinput.Reference

const ReferenceSandboxPathRemovedMessage = queryinput.ReferenceSandboxPathRemovedMessage

type SubmitRequest = queryinput.SubmitRequest

type SubmitParams = queryinput.SubmitParams

func DecodeSubmitParam(raw json.RawMessage) (map[string]any, error) {
	return queryinput.DecodeSubmitParam(raw)
}

func DecodeSubmitParams(params SubmitParams) ([]map[string]any, error) {
	return queryinput.DecodeSubmitParams(params)
}

func EncodeSubmitParams(value any) (SubmitParams, error) { return queryinput.EncodeSubmitParams(value) }

type SubmitResponse = queryinput.SubmitResponse

type SteerRequest = queryinput.SteerRequest

type SteerResponse = queryinput.SteerResponse

type InterruptRequest = queryinput.InterruptRequest

type InterruptResponse = queryinput.InterruptResponse

type CompactRequest struct {
	RequestID string `json:"requestId,omitempty"`
	ChatID    string `json:"chatId,omitempty"`
	AgentKey  string `json:"agentKey,omitempty"`
	Trigger   string `json:"trigger,omitempty"`
	Level     string `json:"level,omitempty"`
}

type CompactResponse = queryinput.CompactResponse

type DetachRequest struct {
	RunID    string `json:"runId"`
	AgentKey string `json:"agentKey,omitempty"`
	TeamID   string `json:"teamId,omitempty"`
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

type AccessLevelRequest = queryinput.AccessLevelRequest

type AccessLevelResponse = queryinput.AccessLevelResponse

type AgentSummary struct {
	Key                    string                     `json:"key"`
	Name                   string                     `json:"name"`
	Icon                   any                        `json:"icon,omitempty"`
	Mode                   string                     `json:"mode,omitempty"`
	Engine                 string                     `json:"engine,omitempty"`
	WorkspaceDir           string                     `json:"workspaceDir,omitempty"`
	AgentConfigDir         string                     `json:"agentConfigDir,omitempty"`
	DefaultModelKey        string                     `json:"defaultModelKey,omitempty"`
	DefaultReasoningEffort string                     `json:"defaultReasoningEffort,omitempty"`
	ModelConfig            map[string]any             `json:"modelConfig,omitempty"`
	ModelOptions           *CoderModelOptionsResponse `json:"modelOptions,omitempty"`
	Description            string                     `json:"-"`
	Role                   string                     `json:"role,omitempty"`
	Stats                  AgentChatStats             `json:"stats"`
	Chats                  []ChatSummaryResponse      `json:"chats,omitempty"`
}

// AgentCatalogSummary is the opt-in, flat union returned by /api/agents when
// includeTeam is enabled. Agent items use kind=agent and preserve the public
// AgentSummary fields. Team items use kind=team and populate the Team-specific
// fields together with the shared Name, Icon, Stats, and Chats fields.
type AgentCatalogSummary struct {
	Kind                   string                     `json:"kind"`
	Key                    string                     `json:"key,omitempty"`
	Name                   string                     `json:"name"`
	Icon                   any                        `json:"icon,omitempty"`
	Mode                   string                     `json:"mode,omitempty"`
	Engine                 string                     `json:"engine,omitempty"`
	WorkspaceDir           string                     `json:"workspaceDir,omitempty"`
	AgentConfigDir         string                     `json:"agentConfigDir,omitempty"`
	DefaultModelKey        string                     `json:"defaultModelKey,omitempty"`
	DefaultReasoningEffort string                     `json:"defaultReasoningEffort,omitempty"`
	ModelConfig            map[string]any             `json:"modelConfig,omitempty"`
	ModelOptions           *CoderModelOptionsResponse `json:"modelOptions,omitempty"`
	Role                   string                     `json:"role,omitempty"`
	Stats                  AgentChatStats             `json:"stats"`
	Chats                  []ChatSummaryResponse      `json:"chats,omitempty"`
	TeamID                 string                     `json:"teamId,omitempty"`
	Description            string                     `json:"description,omitempty"`
	AgentKeys              []string                   `json:"agentKeys,omitempty"`
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

type AgentDetailSkill struct {
	skillmeta.Presentation
	ID          string `json:"id"`
	Name        string `json:"-"` // Internal fallback; public name is displayName.
	Description string `json:"description,omitempty"`
}

type AgentToolBinding struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	Removable bool   `json:"removable"`
	Excluded  bool   `json:"excluded"`
	Active    bool   `json:"active"`
}

type AgentDetailResponse struct {
	ToolBindings      []AgentToolBinding `json:"toolBindings"`
	ModelKey          string             `json:"modelKey,omitempty"`
	ServiceTier       string             `json:"serviceTier,omitempty"`
	ReasoningEffort   string             `json:"reasoningEffort,omitempty"`
	InteractionConfig interaction.Config `json:"interactionConfig"`
	Key               string             `json:"key"`
	Name              string             `json:"name"`
	Icon              any                `json:"icon,omitempty"`
	Description       string             `json:"description,omitempty"`
	Role              string             `json:"role,omitempty"`
	Greetings         []string           `json:"greetings,omitempty"`
	Introductions     []string           `json:"introductions,omitempty"`
	Wonders           []string           `json:"wonders,omitempty"`
	Mode              string             `json:"mode"`
	Engine            string             `json:"engine,omitempty"`
	Tools             []string           `json:"tools"`
	Skills            []AgentDetailSkill `json:"skills"`
	Controls          []map[string]any   `json:"controls"`
	Meta              map[string]any     `json:"meta"`
	Definition        map[string]any     `json:"definition,omitempty"`
	SoulPrompt        string             `json:"soulPrompt,omitempty"`
	AgentsPrompt      string             `json:"agentsPrompt,omitempty"`
	Source            *AgentSource       `json:"source,omitempty"`
}

type AgentSource struct {
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	AgentDir string `json:"agentDir,omitempty"`
}

// AdminSourceTarget identifies one editable, admin-managed text source. It is
// deliberately an identifier-based contract: callers never submit filesystem
// paths, and each source type resolves its own controlled root.
type AdminSourceTarget struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Key      string `json:"key,omitempty"`
	Path     string `json:"path,omitempty"`
	Category string `json:"category,omitempty"`
	File     string `json:"file,omitempty"`
}

type AdminSourceResponse struct {
	Target    AdminSourceTarget `json:"target"`
	Source    AgentSource       `json:"source"`
	Content   string            `json:"content"`
	Encoding  string            `json:"encoding"`
	SHA256    string            `json:"sha256"`
	Size      int64             `json:"size"`
	UpdatedAt int64             `json:"updatedAt,omitempty"`
}

type UpdateAdminSourceRequest struct {
	Target     AdminSourceTarget `json:"target"`
	Content    string            `json:"content"`
	BaseSHA256 string            `json:"baseSha256,omitempty"`
}

type DeleteAdminSourceRequest struct {
	Target     AdminSourceTarget `json:"target"`
	BaseSHA256 string            `json:"baseSha256,omitempty"`
}

type DeleteAdminSourceResponse struct {
	Target  AdminSourceTarget `json:"target"`
	Deleted bool              `json:"deleted"`
}

type AdminAgentDetailResponse struct {
	ToolBindings  []AgentToolBinding       `json:"toolBindings"`
	Key           string                   `json:"key"`
	Name          string                   `json:"name"`
	Icon          any                      `json:"icon,omitempty"`
	Description   string                   `json:"description,omitempty"`
	Role          string                   `json:"role,omitempty"`
	Model         string                   `json:"model,omitempty"`
	Mode          string                   `json:"mode,omitempty"`
	Tools         []string                 `json:"tools"`
	Skills        []string                 `json:"skills"`
	Controls      []map[string]any         `json:"controls"`
	Meta          map[string]any           `json:"meta"`
	Definition    map[string]any           `json:"definition,omitempty"`
	SoulPrompt    string                   `json:"soulPrompt,omitempty"`
	AgentsPrompt  string                   `json:"agentsPrompt,omitempty"`
	Source        *AgentSource             `json:"source,omitempty"`
	Status        string                   `json:"status"`
	Diagnostics   []AdminAgentDiagnostic   `json:"diagnostics,omitempty"`
	PrivateSkills []AdminAgentPrivateSkill `json:"privateSkills,omitempty"`
}

type AdminAgentPrivateSkill struct {
	skillmeta.Presentation
	ID              string                 `json:"id"`
	Name            string                 `json:"-"` // Internal fallback; public name is displayName.
	Description     string                 `json:"description,omitempty"`
	Status          string                 `json:"status"`
	Diagnostics     []AdminAgentDiagnostic `json:"diagnostics,omitempty"`
	Enabled         bool                   `json:"enabled"`
	OverridesCenter bool                   `json:"overridesCenter"`
}

type DeleteAdminAgentPrivateSkillRequest struct {
	AgentKey string `json:"agentKey"`
	ID       string `json:"id"`
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

type MCPServerToolSyncStatus struct {
	Status            string                       `json:"status"`
	LastSyncAttemptAt int64                        `json:"lastSyncAttemptAt,omitempty"`
	LastSyncSuccessAt int64                        `json:"lastSyncSuccessAt,omitempty"`
	Diagnostic        *AdminRegistryListDiagnostic `json:"diagnostic,omitempty"`
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
	ImportCount int                       `json:"importCount"`
	ExportCount int                       `json:"exportCount"`
	Imports     []AdminChannelAgentImport `json:"imports,omitempty"`
	Exports     []AdminChannelAgentExport `json:"exports,omitempty"`
}

type AdminChannelAgentImport struct {
	AgentKey       string `json:"agentKey"`
	Name           string `json:"name,omitempty"`
	RemoteAgentKey string `json:"remoteAgentKey,omitempty"`
}

type AdminChannelAgentExport struct {
	AgentKey         string                        `json:"agentKey"`
	Name             string                        `json:"name,omitempty"`
	ExternalAgentKey string                        `json:"externalAgentKey,omitempty"`
	Allow            AdminChannelAllowFlags        `json:"allow"`
	CardStatus       *GatewayAgentCardReportStatus `json:"cardStatus,omitempty"`
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
	// CapabilityGroups selects creation templates by group key. An absent
	// field leaves the definition as sent; a present field, including an empty
	// list, creates a project agent: base tools are written, the selected
	// groups are expanded, and a project directory and usable model are
	// required.
	CapabilityGroups *[]string `json:"capabilityGroups,omitempty"`
}

type AgentCreationOptionsResponse struct {
	Types  []AgentCreationTypeOption  `json:"types"`
	Groups []AgentCreationGroupOption `json:"groups"`
	Models []AgentEditorModelOption   `json:"models"`
}

type AgentCreationTypeOption struct {
	Key                     string                   `json:"key"`
	Label                   string                   `json:"label"`
	Mode                    string                   `json:"mode"`
	Engine                  string                   `json:"engine"`
	Available               bool                     `json:"available"`
	UnavailableReason       string                   `json:"unavailableReason,omitempty"`
	WorkspaceRequired       bool                     `json:"workspaceRequired"`
	ModelRequired           bool                     `json:"modelRequired"`
	DefaultModelKey         string                   `json:"defaultModelKey,omitempty"`
	DefaultModelAvailable   bool                     `json:"defaultModelAvailable"`
	DefaultReasoningEffort  string                   `json:"defaultReasoningEffort,omitempty"`
	SupportsGroups          bool                     `json:"supportsGroups"`
	GroupsUnsupportedReason string                   `json:"groupsUnsupportedReason,omitempty"`
	BaseTools               []string                 `json:"baseTools"`
	DefaultGroups           []string                 `json:"defaultGroups"`
	ACPBridges              []AgentCreationACPBridge `json:"acpBridges,omitempty"`
}

type AgentCreationACPBridge struct {
	ID string `json:"id"`
}

type AgentCreationGroupOption struct {
	Key               string                `json:"key"`
	Name              string                `json:"name"`
	Description       string                `json:"description,omitempty"`
	Skills            []AgentCreationMember `json:"skills"`
	Tools             []string              `json:"tools"`
	Connectors        []AgentCreationMember `json:"connectors"`
	Available         bool                  `json:"available"`
	UnavailableReason string                `json:"unavailableReason,omitempty"`
}

// HostDirectoryListResponse lists the sub-directories of one directory on the
// Platform host, so a remote client can choose a project directory that the
// Platform itself can reach. It never returns files or file contents.
type HostDirectoryListResponse struct {
	Path      string               `json:"path"`
	Parent    string               `json:"parent,omitempty"`
	Home      string               `json:"home,omitempty"`
	Separator string               `json:"separator"`
	Entries   []HostDirectoryEntry `json:"entries"`
	Truncated bool                 `json:"truncated"`
}

type HostDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type AgentCreationMember struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type UpdateAgentRequest struct {
	Key          string         `json:"key"`
	AgentKey     string         `json:"agentKey,omitempty"`
	Definition   map[string]any `json:"definition"`
	SoulPrompt   *string        `json:"soulPrompt,omitempty"`
	AgentsPrompt *string        `json:"agentsPrompt,omitempty"`
}

type UpdateAgentModelConfigRequest struct {
	AgentKey        string          `json:"agentKey"`
	ModelKey        *string         `json:"modelKey,omitempty"`
	ReasoningEffort *string         `json:"reasoningEffort,omitempty"`
	ServiceTier     json.RawMessage `json:"serviceTier,omitempty"`
}

func (r *UpdateAgentModelConfigRequest) UnmarshalJSON(data []byte) error {
	type plain UpdateAgentModelConfigRequest
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"modelKey", "reasoningEffort"} {
		if raw, exists := fields[key]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s must not be null", key)
		}
	}
	*r = UpdateAgentModelConfigRequest(decoded)
	return nil
}

type AgentModelConfigResponse struct {
	AgentKey        string `json:"agentKey"`
	ModelKey        string `json:"modelKey"`
	ReasoningEffort string `json:"reasoningEffort"`
	ServiceTier     string `json:"serviceTier,omitempty"`
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

type UpdateAgentSkillPinRequest struct {
	ID     string `json:"id"`
	Pinned *bool  `json:"pinned"`
}

type AgentOrderResponse struct {
	Version   int      `json:"version"`
	Order     []string `json:"order"`
	UpdatedAt *int64   `json:"updatedAt,omitempty"`
}

type UpdateAgentOrderRequest struct {
	Order []string `json:"order"`
}

type OpenAgentDirectoryRequest struct {
	AgentKey      string `json:"agentKey"`
	DirectoryType string `json:"directoryType"`
}

type OpenAgentDirectoryResponse struct {
	AgentKey      string `json:"agentKey"`
	DirectoryType string `json:"directoryType"`
	DirectoryPath string `json:"directoryPath"`
	Opened        bool   `json:"opened"`
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
	Key              string   `json:"key"`
	Name             string   `json:"name,omitempty"`
	Icon             string   `json:"icon,omitempty"`
	Provider         string   `json:"provider,omitempty"`
	ModelID          string   `json:"modelId,omitempty"`
	Protocol         string   `json:"protocol,omitempty"`
	IsVision         bool     `json:"isVision"`
	ContextWindow    int      `json:"contextWindow,omitempty"`
	Timeout          int      `json:"timeout,omitempty"`
	ReasoningEfforts []string `json:"reasoningEfforts"`
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
	Models           []CoderModelOption      `json:"models"`
	ReasoningEfforts []ReasoningEffortOption `json:"reasoningEfforts"`
	ServiceTiers     []ServiceTierOption     `json:"serviceTiers,omitempty"`
}

type CoderModelOption = queryinput.CoderModelOption

type ReasoningEffortOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type ServiceTierOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type TeamSummary struct {
	TeamID      string         `json:"teamId"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Icon        any            `json:"icon,omitempty"`
	AgentKeys   []string       `json:"agentKeys"`
	Meta        map[string]any `json:"meta,omitempty"`
}

type SkillSummary struct {
	skillmeta.Presentation
	ID          string         `json:"id"`
	Name        string         `json:"-"` // Internal fallback; public name is displayName.
	Description string         `json:"description,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

type AgentSkillResponse struct {
	skillmeta.Presentation
	Icon        string `json:"icon,omitempty"`
	ID          string `json:"id"`
	Name        string `json:"-"` // Internal fallback; public name is displayName.
	Description string `json:"description,omitempty"`
	Configured  bool   `json:"configured"`
}

// AgentSkillPackageResponse groups selectable shared skills without changing their IDs.
type AgentSkillPackageResponse struct {
	Icon string `json:"icon,omitempty"`
	skillmeta.Presentation
	Description     string                   `json:"description,omitempty"`
	Triggers        []string                 `json:"triggers,omitempty"`
	ID              string                   `json:"id"`
	Name            string                   `json:"name"`
	Skills          []AdminSkillPackageSkill `json:"skills"`
	MissingSkillIDs []string                 `json:"missingSkillIds"`
	Status          string                   `json:"status"`
}

type AgentSkillsResponse struct {
	Packages []AgentSkillPackageResponse `json:"packages,omitempty"`
	Pinned   []string                    `json:"pinned"`
	AgentKey string                      `json:"agentKey"`
	Skills   []AgentSkillResponse        `json:"skills"`
}

// AdminSkillsResponse is the HTTP management catalog and current user's pins.
type AdminSkillsResponse struct {
	Skills   []AdminSkillSummary         `json:"skills"`
	Packages []AdminSkillPackageResponse `json:"packages"`
	Pinned   []string                    `json:"pinned"`
}
type AdminSkillPinResponse struct {
	Pinned []string `json:"pinned"`
}

type AdminSkillSummary struct {
	skillmeta.Presentation
	PackageID       string                       `json:"packageId,omitempty"`
	ID              string                       `json:"id"`
	Name            string                       `json:"-"` // Internal fallback; public name is displayName.
	Description     string                       `json:"description,omitempty"`
	Icon            string                       `json:"icon,omitempty"`
	Meta            map[string]any               `json:"meta,omitempty"`
	Status          string                       `json:"status"`
	Diagnostic      *AdminRegistryListDiagnostic `json:"diagnostic,omitempty"`
	DiagnosticCount int                          `json:"diagnosticCount,omitempty"`
	UpdatedAt       int64                        `json:"updatedAt,omitempty"`
	Size            int64                        `json:"size,omitempty"`
	UsedByAgents    []string                     `json:"usedByAgents,omitempty"`
	Source          *AgentSource                 `json:"source,omitempty"`
}

type AdminSkillDetailResponse struct {
	Skill        AdminSkillSummary      `json:"skill"`
	Capabilities AdminSkillCapabilities `json:"capabilities"`
	FileManifest AdminSkillFileManifest `json:"fileManifest"`
	Diagnostics  []AdminAgentDiagnostic `json:"diagnostics,omitempty"`
	OpenedFile   *AdminSkillTextFile    `json:"openedFile,omitempty"`
}

type AdminSkillInlineFile struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Encoding string `json:"encoding,omitempty"`
}

type CreateAdminSkillRequest struct {
	ID      string                 `json:"id"`
	SkillMd string                 `json:"skillMd"`
	Files   []AdminSkillInlineFile `json:"files,omitempty"`
}

type DeleteAdminSkillRequest struct {
	ID string `json:"id"`
}

type DeleteAdminSkillResponse struct {
	ID           string   `json:"id"`
	Deleted      bool     `json:"deleted"`
	UsedByAgents []string `json:"usedByAgents,omitempty"`
}

type AdminSkillPackageSkill struct {
	Diagnostics []AdminAgentDiagnostic `json:"diagnostics,omitempty"`
	ID          string                 `json:"id"`
	Version     string                 `json:"version,omitempty"`
}

// AdminSkillImportResponse keeps single-skill detail fields at the top level
// for existing clients and identifies package imports explicitly.
type AdminSkillImportResponse struct {
	Kind string `json:"kind"`
	*AdminSkillDetailResponse
	Package *AdminSkillPackageResponse `json:"package,omitempty"`
}

type AdminSkillPackageResponse struct {
	Icon string `json:"icon,omitempty"`
	skillmeta.Presentation
	Description     string                   `json:"description,omitempty"`
	Triggers        []string                 `json:"triggers,omitempty"`
	Status          string                   `json:"status,omitempty"`
	MissingSkillIDs []string                 `json:"missingSkillIds"`
	Name            string                   `json:"name,omitempty"`
	ID              string                   `json:"id"`
	SHA256          string                   `json:"sha256"`
	Skills          []AdminSkillPackageSkill `json:"skills"`
	InstalledAt     int64                    `json:"installedAt"`
}

type DeleteAdminSkillPackageRequest struct {
	ID string `json:"id"`
}

type DeleteAdminSkillPackageResponse struct {
	ID      string                   `json:"id"`
	Deleted bool                     `json:"deleted"`
	Skills  []AdminSkillPackageSkill `json:"skills"`
}

type DeleteAdminSkillPackageSkillRequest struct {
	PackageID string `json:"packageId"`
	SkillID   string `json:"skillId"`
}

type DeleteAdminSkillPackageSkillResponse struct {
	PackageID       string                   `json:"packageId"`
	SkillID         string                   `json:"skillId"`
	Deleted         bool                     `json:"deleted"`
	PackageDeleted  bool                     `json:"packageDeleted"`
	RemainingSkills []AdminSkillPackageSkill `json:"remainingSkills"`
}

type WriteAdminSkillFileRequest struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Content    string `json:"content"`
	Encoding   string `json:"encoding,omitempty"`
	BaseSHA256 string `json:"baseSha256,omitempty"`
}

type DeleteAdminSkillFileRequest struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Recursive  bool   `json:"recursive,omitempty"`
	BaseSHA256 string `json:"baseSha256,omitempty"`
}

type MkdirAdminSkillFileRequest struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type RenameAdminSkillFileRequest struct {
	ID        string `json:"id"`
	FromPath  string `json:"fromPath"`
	ToPath    string `json:"toPath"`
	Overwrite bool   `json:"overwrite,omitempty"`
}

type AdminSkillCapabilities struct {
	MaxTextBytes   int64 `json:"maxTextBytes"`
	MaxUploadBytes int64 `json:"maxUploadBytes"`
	CanCreate      bool  `json:"canCreate"`
	CanRename      bool  `json:"canRename"`
	CanDelete      bool  `json:"canDelete"`
	CanUpload      bool  `json:"canUpload"`
	CanDownload    bool  `json:"canDownload"`
}

type AdminSkillFileManifest struct {
	Revision        string                `json:"revision"`
	DefaultOpenPath string                `json:"defaultOpenPath,omitempty"`
	Counts          AdminSkillFileCounts  `json:"counts"`
	Entries         []AdminSkillFileEntry `json:"entries"`
}

type AdminSkillFileCounts struct {
	Files       int   `json:"files"`
	Directories int   `json:"directories"`
	TextFiles   int   `json:"textFiles"`
	BinaryFiles int   `json:"binaryFiles"`
	TotalSize   int64 `json:"totalSize"`
}

type AdminSkillFileEntry struct {
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

type AdminSkillTextFile struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Encoding  string `json:"encoding"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	Editable  bool   `json:"editable"`
}

type CreateAdminSkillFileRequest struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Content  string `json:"content,omitempty"`
	Encoding string `json:"encoding,omitempty"`
}

type AdminSkillMutationResponse struct {
	ID           string                  `json:"id"`
	Action       string                  `json:"action"`
	SelectedPath string                  `json:"selectedPath,omitempty"`
	Entry        *AdminSkillFileEntry    `json:"entry,omitempty"`
	OpenedFile   *AdminSkillTextFile     `json:"openedFile,omitempty"`
	FileManifest *AdminSkillFileManifest `json:"fileManifest,omitempty"`
	Skill        *AdminSkillSummary      `json:"skill,omitempty"`
	Diagnostics  []AdminAgentDiagnostic  `json:"diagnostics,omitempty"`
	Reloaded     bool                    `json:"reloaded"`
}

type ValidateAdminSkillRequest struct {
	ID string `json:"id"`
}

type AdminSkillValidateResponse struct {
	ID          string                 `json:"id"`
	Status      string                 `json:"status"`
	Diagnostics []AdminAgentDiagnostic `json:"diagnostics,omitempty"`
	UpdatedAt   int64                  `json:"updatedAt,omitempty"`
	Size        int64                  `json:"size,omitempty"`
}

type ToolSummary struct {
	ToolI18n       map[string]any `json:"toolI18n,omitempty"`
	Key            string         `json:"key"`
	Name           string         `json:"name"`
	Label          string         `json:"label,omitempty"`
	Description    string         `json:"description,omitempty"`
	SourceType     string         `json:"sourceType"`
	SourceCategory string         `json:"sourceCategory"`
	ServerKey      string         `json:"serverKey,omitempty"`
	MCPToolName    string         `json:"mcpToolName,omitempty"`
}

type ToolDetailResponse = queryinput.ToolDefinition

type ChatSummaryResponse struct {
	CanContinue    bool           `json:"canContinue"`
	Pinned         bool           `json:"pinned"`
	ChatID         string         `json:"chatId"`
	ChatName       string         `json:"chatName"`
	AgentKey       string         `json:"agentKey,omitempty"`
	Mode           string         `json:"mode,omitempty"`
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

type ChatOrderResponse struct {
	PinnedOrder []string `json:"pinnedOrder"`
	SortMode    string   `json:"sortMode"`
	UpdatedAt   *int64   `json:"updatedAt,omitempty"`
}

// ChatOrderSnapshotResponse is the read contract. Mutations retain the lightweight acknowledgement.
type ChatOrderSnapshotResponse struct {
	ChatOrderResponse
	PinnedChats []ChatSummaryResponse `json:"pinnedChats"`
}

type UpdateChatOrderRequest struct {
	Pinned       *bool  `json:"pinned,omitempty"`
	Operation    string `json:"operation"`
	SortMode     string `json:"sortMode,omitempty"`
	ChatID       string `json:"chatId,omitempty"`
	BeforeChatID string `json:"beforeChatId,omitempty"`
	AfterChatID  string `json:"afterChatId,omitempty"`
}

type ChatErrorInfo = queryinput.ChatErrorInfo

type ChatReadState struct {
	IsRead    bool   `json:"isRead"`
	ReadAt    *int64 `json:"readAt,omitempty"`
	ReadRunID string `json:"readRunId,omitempty"`
}

type Awaiting = queryinput.Awaiting

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
	CanContinue    bool                `json:"canContinue"`
	Pinned         bool                `json:"pinned"`
	ChatID         string              `json:"chatId"`
	ChatName       string              `json:"chatName"`
	AgentKey       string              `json:"agentKey,omitempty"`
	Mode           string              `json:"mode,omitempty"`
	TeamID         string              `json:"teamId,omitempty"`
	CreatedAt      int64               `json:"createdAt"`
	UpdatedAt      int64               `json:"updatedAt"`
	LastRunID      string              `json:"lastRunId,omitempty"`
	LastRunContent string              `json:"lastRunContent,omitempty"`
	Read           ChatReadState       `json:"read"`
	Source         string              `json:"source,omitempty"`
	ResourceTicket string              `json:"resourceTicket,omitempty"`
	RawMessages    []map[string]any    `json:"rawMessages,omitempty"`
	Events         []stream.EventData  `json:"events"`
	Runs           []RunSummary        `json:"runs,omitempty"`
	Awaiting       *Awaiting           `json:"awaiting,omitempty"`
	ActiveRun      *ActiveRunInfo      `json:"activeRun,omitempty"`
	Plan           any                 `json:"plan,omitempty"`
	Planning       any                 `json:"planning,omitempty"`
	Artifact       any                 `json:"artifact,omitempty"`
	References     []Reference         `json:"references,omitempty"`
	Usage          *ChatUsageBreakdown `json:"usage,omitempty"`
	ContextWindow  *ChatContextWindow  `json:"contextWindow,omitempty"`
}

// ChatSystemPromptResponse exposes the initial persisted system message used
// by an agent within a run. It intentionally omits the snapshot's tools,
// model, and request options: callers asking to view the system prompt do not
// need the rest of the LLM execution profile.
type ChatSystemPromptResponse struct {
	ChatID        string              `json:"chatId"`
	RunID         string              `json:"runId"`
	AgentKey      string              `json:"agentKey"`
	SystemRef     ChatSystemPromptRef `json:"systemRef"`
	SystemMessage map[string]any      `json:"systemMessage"`
}

type ChatSystemPromptRef struct {
	AgentKey    string `json:"agentKey"`
	CacheKey    string `json:"cacheKey"`
	Fingerprint string `json:"fingerprint"`
}

type ArchivedChatDetailResponse struct {
	ChatID                      string             `json:"chatId"`
	ChatName                    string             `json:"chatName"`
	CreatedAt                   int64              `json:"createdAt"`
	LastRunAt                   int64              `json:"lastRunAt"`
	ArchivedAt                  int64              `json:"archivedAt"`
	ResourceTicket              string             `json:"resourceTicket,omitempty"`
	RawMessages                 []map[string]any   `json:"rawMessages,omitempty"`
	Events                      []stream.EventData `json:"events"`
	Runs                        []RunSummary       `json:"runs,omitempty"`
	ActiveRun                   *ActiveRunInfo     `json:"activeRun,omitempty"`
	Plan                        any                `json:"plan,omitempty"`
	Planning                    any                `json:"planning,omitempty"`
	Artifact                    any                `json:"artifact,omitempty"`
	ArtifactManifestUnavailable bool               `json:"artifactManifestUnavailable,omitempty"`
	References                  []Reference        `json:"references,omitempty"`
	Usage                       *ChatUsageData     `json:"usage,omitempty"`
}

type RunSummary struct {
	RunID           string        `json:"runId"`
	ChatID          string        `json:"chatId"`
	AgentKey        string        `json:"agentKey,omitempty"`
	Mode            string        `json:"mode,omitempty"`
	TeamID          string        `json:"teamId,omitempty"`
	InitialMessage  string        `json:"initialMessage,omitempty"`
	AssistantText   string        `json:"assistantText,omitempty"`
	FinishReason    string        `json:"finishReason,omitempty"`
	StartedAt       int64         `json:"startedAt"`
	CompletedAt     *int64        `json:"completedAt,omitempty"`
	Usage           ChatUsageData `json:"usage"`
	FeedbackType    string        `json:"feedbackType,omitempty"`
	FeedbackComment string        `json:"feedbackComment,omitempty"`
	FeedbackAt      int64         `json:"feedbackAt,omitempty"`
}

type ActiveRunInfo struct {
	RunID        string `json:"runId"`
	AgentKey     string `json:"agentKey,omitempty"`
	TeamID       string `json:"teamId,omitempty"`
	State        string `json:"state"`
	LastSeq      int64  `json:"lastSeq"`
	OldestSeq    int64  `json:"oldestSeq"`
	StartedAt    int64  `json:"startedAt"`
	PlanningMode bool   `json:"planningMode,omitempty"`
	EditingMode  bool   `json:"editingMode,omitempty"`
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
	SetAt  *int64 `json:"setAt,omitempty"`
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

type ConnectorOrderResponse struct {
	Version   int      `json:"version"`
	Order     []string `json:"order"`
	UpdatedAt *int64   `json:"updatedAt,omitempty"`
}

type UpdateConnectorOrderRequest struct {
	ID     string `json:"id"`
	Pinned *bool  `json:"pinned"`
}
