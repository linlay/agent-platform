package api

type AutomationListRequest struct {
	Tag string `json:"tag,omitempty"`
}

type AutomationListResponse struct {
	Items            []AutomationSummaryResponse      `json:"items"`
	Total            int                              `json:"total"`
	ExecutionHistory AutomationExecutionHistoryStatus `json:"executionHistory"`
}

type AutomationExecutionListResponse struct {
	Items []AutomationExecutionResponse `json:"items"`
	Total int                           `json:"total"`
}

type AutomationSummaryResponse struct {
	ID            string                    `json:"id"`
	Name          string                    `json:"name"`
	Description   string                    `json:"description,omitempty"`
	Cron          string                    `json:"cron"`
	AgentKey      string                    `json:"agentKey,omitempty"`
	Enabled       bool                      `json:"enabled"`
	TeamID        string                    `json:"teamId,omitempty"`
	ZoneID        string                    `json:"zoneId,omitempty"`
	SourceFile    string                    `json:"sourceFile,omitempty"`
	RemainingRuns *int                      `json:"remainingRuns,omitempty"`
	NextFireAt    *int64                    `json:"nextFireAt,omitempty"`
	NextFireTime  *string                   `json:"nextFireTime,omitempty"`
	LastExecution *AutomationExecutionBrief `json:"lastExecution,omitempty"`
}

type AutomationDetailResponse struct {
	AutomationSummaryResponse
	Query            AutomationQueryResponse          `json:"query"`
	ExecutionHistory AutomationExecutionHistoryStatus `json:"executionHistory"`
}

type AutomationExecutionHistoryStatus struct {
	Available bool   `json:"available"`
	State     string `json:"state"`
	Message   string `json:"message,omitempty"`
}

type AutomationQueryResponse struct {
	Message string         `json:"message"`
	ChatID  string         `json:"chatId,omitempty"`
	Role    string         `json:"role,omitempty"`
	Hidden  *bool          `json:"hidden,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
}

type AutomationExecutionBrief struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	ZoneID        string `json:"zoneId"`
	ChatID        string `json:"chatId,omitempty"`
	RunID         string `json:"runId,omitempty"`
	FinishReason  string `json:"finishReason,omitempty"`
	HasResult     bool   `json:"hasResult"`
	ResultPreview string `json:"resultPreview,omitempty"`
	StartedAt     int64  `json:"startedAt"`
	StartedTime   string `json:"startedTime"`
	RunStartedAt  *int64 `json:"runStartedAt,omitempty"`
	CompletedAt   *int64 `json:"completedAt,omitempty"`
	CompletedTime string `json:"completedTime,omitempty"`
	DurationMs    *int64 `json:"durationMs,omitempty"`
	Error         string `json:"error,omitempty"`
}

type AutomationExecutionResponse struct {
	ID             string `json:"id"`
	AutomationID   string `json:"automationId"`
	AutomationName string `json:"automationName"`
	SourceFile     string `json:"sourceFile"`
	AgentKey       string `json:"agentKey,omitempty"`
	TeamID         string `json:"teamId,omitempty"`
	Status         string `json:"status"`
	Error          string `json:"error"`
	ZoneID         string `json:"zoneId"`
	ChatID         string `json:"chatId,omitempty"`
	RunID          string `json:"runId,omitempty"`
	FinishReason   string `json:"finishReason,omitempty"`
	HasResult      bool   `json:"hasResult"`
	ResultPreview  string `json:"resultPreview,omitempty"`
	StartedAt      int64  `json:"startedAt"`
	StartedTime    string `json:"startedTime"`
	RunStartedAt   *int64 `json:"runStartedAt,omitempty"`
	CompletedAt    *int64 `json:"completedAt,omitempty"`
	CompletedTime  string `json:"completedTime,omitempty"`
	DurationMs     *int64 `json:"durationMs,omitempty"`
}

type AutomationExecutionDetailResponse struct {
	AutomationExecutionResponse
	QueryContent  string `json:"queryContent"`
	ResultContent string `json:"resultContent"`
}

type CreateAutomationRequest struct {
	Name          string                 `json:"name"`
	Description   string                 `json:"description,omitempty"`
	Cron          string                 `json:"cron"`
	AgentKey      string                 `json:"agentKey,omitempty"`
	Enabled       *bool                  `json:"enabled,omitempty"`
	TeamID        string                 `json:"teamId,omitempty"`
	ZoneID        string                 `json:"zoneId,omitempty"`
	RemainingRuns *int                   `json:"remainingRuns,omitempty"`
	Query         AutomationQueryRequest `json:"query"`
}

type AutomationQueryRequest struct {
	Message string         `json:"message"`
	ChatID  string         `json:"chatId,omitempty"`
	Role    string         `json:"role,omitempty"`
	Hidden  *bool          `json:"hidden,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
}

type UpdateAutomationRequest struct {
	ID            string                  `json:"id"`
	AutomationID  string                  `json:"automationId,omitempty"`
	Name          *string                 `json:"name,omitempty"`
	Description   *string                 `json:"description,omitempty"`
	Cron          *string                 `json:"cron,omitempty"`
	AgentKey      *string                 `json:"agentKey,omitempty"`
	TeamID        *string                 `json:"teamId,omitempty"`
	ZoneID        *string                 `json:"zoneId,omitempty"`
	Enabled       *bool                   `json:"enabled,omitempty"`
	RemainingRuns *int                    `json:"remainingRuns,omitempty"`
	Query         *AutomationQueryRequest `json:"query,omitempty"`
}

type ToggleAutomationRequest struct {
	ID           string `json:"id"`
	AutomationID string `json:"automationId,omitempty"`
	Enabled      bool   `json:"enabled"`
}

type DeleteAutomationRequest struct {
	ID           string `json:"id"`
	AutomationID string `json:"automationId,omitempty"`
}

type AutomationExecutionsRequest struct {
	ID           string `json:"id"`
	AutomationID string `json:"automationId,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Offset       int    `json:"offset,omitempty"`
}

type AutomationExecutionRequest struct {
	ExecutionID string `json:"executionId"`
	ID          string `json:"id,omitempty"`
}
