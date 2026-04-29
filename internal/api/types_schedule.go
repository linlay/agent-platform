package api

type ScheduleListRequest struct {
	Tag string `json:"tag,omitempty"`
}

type ScheduleListResponse struct {
	Items []ScheduleSummaryResponse `json:"items"`
	Total int                       `json:"total"`
}

type ScheduleExecutionListResponse struct {
	Items []ScheduleExecutionResponse `json:"items"`
	Total int                         `json:"total"`
}

type ScheduleSummaryResponse struct {
	ID            string                  `json:"id"`
	Name          string                  `json:"name"`
	Description   string                  `json:"description"`
	Cron          string                  `json:"cron"`
	AgentKey      string                  `json:"agentKey"`
	Enabled       bool                    `json:"enabled"`
	TeamID        string                  `json:"teamId,omitempty"`
	ZoneID        string                  `json:"zoneId,omitempty"`
	SourceFile    string                  `json:"sourceFile,omitempty"`
	RemainingRuns *int                    `json:"remainingRuns,omitempty"`
	NextFireTime  *string                 `json:"nextFireTime,omitempty"`
	LastExecution *ScheduleExecutionBrief `json:"lastExecution,omitempty"`
}

type ScheduleDetailResponse struct {
	ScheduleSummaryResponse
	Query ScheduleQueryResponse `json:"query"`
}

type ScheduleQueryResponse struct {
	Message string         `json:"message"`
	ChatID  string         `json:"chatId,omitempty"`
	Role    string         `json:"role,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
	Hidden  *bool          `json:"hidden,omitempty"`
}

type ScheduleExecutionBrief struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	StartedAt  int64  `json:"startedAt"`
	DurationMs *int64 `json:"durationMs,omitempty"`
	Error      string `json:"error,omitempty"`
}

type ScheduleExecutionResponse struct {
	ID           string `json:"id"`
	ScheduleID   string `json:"scheduleId"`
	ScheduleName string `json:"scheduleName"`
	SourceFile   string `json:"sourceFile"`
	AgentKey     string `json:"agentKey"`
	TeamID       string `json:"teamId"`
	Status       string `json:"status"`
	Error        string `json:"error"`
	StartedAt    int64  `json:"startedAt"`
	CompletedAt  *int64 `json:"completedAt,omitempty"`
	DurationMs   *int64 `json:"durationMs,omitempty"`
}

type CreateScheduleRequest struct {
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	Cron          string               `json:"cron"`
	AgentKey      string               `json:"agentKey"`
	Enabled       *bool                `json:"enabled,omitempty"`
	TeamID        string               `json:"teamId,omitempty"`
	ZoneID        string               `json:"zoneId,omitempty"`
	RemainingRuns *int                 `json:"remainingRuns,omitempty"`
	Query         ScheduleQueryRequest `json:"query"`
}

type ScheduleQueryRequest struct {
	Message string         `json:"message"`
	ChatID  string         `json:"chatId,omitempty"`
	Role    string         `json:"role,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
	Hidden  *bool          `json:"hidden,omitempty"`
}

type UpdateScheduleRequest struct {
	ID            string                `json:"id"`
	Name          *string               `json:"name,omitempty"`
	Description   *string               `json:"description,omitempty"`
	Cron          *string               `json:"cron,omitempty"`
	AgentKey      *string               `json:"agentKey,omitempty"`
	TeamID        *string               `json:"teamId,omitempty"`
	ZoneID        *string               `json:"zoneId,omitempty"`
	Enabled       *bool                 `json:"enabled,omitempty"`
	RemainingRuns *int                  `json:"remainingRuns,omitempty"`
	Query         *ScheduleQueryRequest `json:"query,omitempty"`
}

type ToggleScheduleRequest struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

type DeleteScheduleRequest struct {
	ID string `json:"id"`
}

type ScheduleExecutionsRequest struct {
	ID     string `json:"id"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}
