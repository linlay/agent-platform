package api

type ApiResponse[T any] struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data T      `json:"data"`
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

type QueryRequest struct {
	RequestID  string         `json:"requestId,omitempty"`
	ChatID     string         `json:"chatId,omitempty"`
	AgentKey   string         `json:"agentKey,omitempty"`
	TeamID     string         `json:"teamId,omitempty"`
	Role       string         `json:"role,omitempty"`
	Message    string         `json:"message"`
	References []Reference    `json:"references,omitempty"`
	Params     map[string]any `json:"params,omitempty"`
	Scene      *Scene         `json:"scene,omitempty"`
	Stream     *bool          `json:"stream,omitempty"`
	Hidden     *bool          `json:"hidden,omitempty"`
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
	RunID  string `json:"runId"`
	ToolID string `json:"toolId"`
	Params any    `json:"params"`
}

type SubmitResponse struct {
	Accepted bool   `json:"accepted"`
	Status   string `json:"status"`
	RunID    string `json:"runId"`
	ToolID   string `json:"toolId"`
	Detail   string `json:"detail"`
}

type SteerRequest struct {
	RequestID    string `json:"requestId,omitempty"`
	ChatID       string `json:"chatId,omitempty"`
	RunID        string `json:"runId"`
	SteerID      string `json:"steerId,omitempty"`
	AgentKey     string `json:"agentKey,omitempty"`
	TeamID       string `json:"teamId,omitempty"`
	Message      string `json:"message"`
	PlanningMode *bool  `json:"planningMode,omitempty"`
}

type SteerResponse struct {
	Accepted bool   `json:"accepted"`
	Status   string `json:"status"`
	RunID    string `json:"runId"`
	SteerID  string `json:"steerId"`
	Detail   string `json:"detail"`
}

type InterruptRequest struct {
	RequestID    string `json:"requestId,omitempty"`
	ChatID       string `json:"chatId,omitempty"`
	RunID        string `json:"runId"`
	AgentKey     string `json:"agentKey,omitempty"`
	TeamID       string `json:"teamId,omitempty"`
	Message      string `json:"message,omitempty"`
	PlanningMode *bool  `json:"planningMode,omitempty"`
}

type InterruptResponse struct {
	Accepted bool   `json:"accepted"`
	Status   string `json:"status"`
	RunID    string `json:"runId"`
	Detail   string `json:"detail"`
}

type LearnRequest struct {
	RequestID  string `json:"requestId,omitempty"`
	ChatID     string `json:"chatId"`
	SubjectKey string `json:"subjectKey,omitempty"`
}

type LearnResponse struct {
	Accepted  bool   `json:"accepted"`
	Status    string `json:"status"`
	RequestID string `json:"requestId,omitempty"`
	ChatID    string `json:"chatId"`
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
	ID         string   `json:"id"`
	RequestID  string   `json:"requestId,omitempty"`
	ChatID     string   `json:"chatId"`
	AgentKey   string   `json:"agentKey,omitempty"`
	SubjectKey string   `json:"subjectKey,omitempty"`
	Summary    string   `json:"summary"`
	SourceType string   `json:"sourceType"`
	Category   string   `json:"category"`
	Importance int      `json:"importance"`
	Tags       []string `json:"tags,omitempty"`
	CreatedAt  int64    `json:"createdAt"`
	UpdatedAt  int64    `json:"updatedAt"`
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

type AgentSummary struct {
	Key         string         `json:"key"`
	Name        string         `json:"name"`
	Icon        any            `json:"icon,omitempty"`
	Description string         `json:"description,omitempty"`
	Role        string         `json:"role,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
}

type AgentDetailResponse struct {
	Key         string           `json:"key"`
	Name        string           `json:"name"`
	Icon        any              `json:"icon,omitempty"`
	Description string           `json:"description,omitempty"`
	Role        string           `json:"role,omitempty"`
	Model       string           `json:"model"`
	Mode        string           `json:"mode"`
	Tools       []string         `json:"tools"`
	Skills      []string         `json:"skills"`
	Controls    []map[string]any `json:"controls"`
	Meta        map[string]any   `json:"meta"`
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

type MarkChatReadRequest struct {
	ChatID string `json:"chatId"`
}

type MarkChatReadResponse struct {
	ChatID     string `json:"chatId"`
	ReadStatus int    `json:"readStatus"`
	ReadAt     int64  `json:"readAt"`
}

type ChatDetailResponse struct {
	ChatID         string           `json:"chatId"`
	ChatName       string           `json:"chatName"`
	ChatImageToken string           `json:"chatImageToken,omitempty"`
	RawMessages    []map[string]any `json:"rawMessages,omitempty"`
	Events         []map[string]any `json:"events"`
	Plan           any              `json:"plan,omitempty"`
	Artifact       any              `json:"artifact,omitempty"`
	References     []Reference      `json:"references,omitempty"`
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
	MimeType  string `json:"mimeType,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256,omitempty"`
}
