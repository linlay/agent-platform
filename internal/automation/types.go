package automation

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
)

const (
	ExecutionStatusRunning  = "running"
	ExecutionStatusSuccess  = "success"
	ExecutionStatusFailed   = "failed"
	ExecutionStatusCanceled = "canceled"
)

type Definition struct {
	ID            string
	Name          string
	Description   string
	Enabled       bool
	Cron          string
	RemainingRuns *int
	AgentKey      string

	Environment Environment
	Query       Query
	SourceFile  string
}

type Execution struct {
	ID             string
	AutomationID   string
	AutomationName string
	SourceFile     string
	AgentKey       string

	ZoneID        string
	QueryContent  string
	ChatID        string
	RunID         string
	Status        string
	FinishReason  string
	ResultContent string
	ResultPreview string
	Error         string
	StartedAt     int64
	RunStartedAt  *int64
	CompletedAt   *int64
	DurationMs    *int64
}

type QueryRunHooks struct {
	OnRunStarted func(chat.RunStart)
}

type QueryRunResult struct {
	Completion   *chat.RunCompletion
	ErrorMessage string
}

type DispatchFunc func(context.Context, api.QueryRequest, QueryRunHooks) (QueryRunResult, error)

type ExecutionRecorder interface {
	Submit(Execution)
}

type ExecutionHistoryState string

const (
	ExecutionHistoryInitializing ExecutionHistoryState = "initializing"
	ExecutionHistoryReady        ExecutionHistoryState = "ready"
	ExecutionHistoryDegraded     ExecutionHistoryState = "degraded"
	ExecutionHistoryUnavailable  ExecutionHistoryState = "unavailable"
)

type ExecutionHistoryStatus struct {
	Available bool
	State     ExecutionHistoryState
	Message   string
}

type ExecutionHistoryReader interface {
	Status() ExecutionHistoryStatus
	ListByAutomation(automationID string, limit, offset int) ([]Execution, int, error)
	LastExecution(automationID string) (*Execution, error)
	GetExecution(executionID string) (*Execution, error)
}

type AutomationInfo struct {
	Definition   Definition
	NextFireTime time.Time
}

type Environment struct {
	ZoneID string
}

type Query struct {
	// OriginalInput retains the configured fields before normalization.
	OriginalInput map[string]any
	// Role is kept as configured; execution ignores it and marks the input by
	// its automation origin instead.
	Role        string
	AccessLevel string
	RequestID   string
	ChatID      string
	Hidden      *bool
	Message     string
	References  []api.Reference
	Params      map[string]any
	Scene       *api.Scene
}

func (d Definition) ToQueryRequest() api.QueryRequest {
	params := contracts.CloneMap(d.Query.Params)
	hidden := EffectiveQueryHidden(d.Query.Hidden)
	chatSource := ""
	if id := strings.TrimSpace(d.ID); id != "" {
		chatSource = api.ChatSourceAutomationPrefix + id
	}

	originalQuery, _ := json.Marshal(d.Query.Input())
	return api.QueryRequest{
		OriginalQuery: originalQuery,
		AccessLevel:   d.Query.AccessLevel,
		RequestID:     d.Query.RequestID,
		ChatID:        d.Query.ChatID,
		AgentKey:      d.AgentKey,
		Origin:        &queryinput.Origin{Type: queryinput.OriginAutomation, AutomationID: strings.TrimSpace(d.ID)},

		Hidden:     &hidden,
		Message:    d.Query.Message,
		References: append([]api.Reference(nil), d.Query.References...),
		Params:     params,
		Scene:      cloneScene(d.Query.Scene),
		ChatSource: chatSource,
	}
}

func EffectiveQueryHidden(hidden *bool) bool {
	return hidden == nil || *hidden
}

// Input is the query exactly as the definition configures it: omitted fields
// stay omitted, so the recorded input carries no computed defaults.
func (q Query) Input() map[string]any {
	if q.OriginalInput != nil {
		return contracts.CloneAnyMap(q.OriginalInput)
	}
	input := map[string]any{"message": q.Message}
	if q.Role != "" {
		input["role"] = q.Role
	}
	if q.AccessLevel != "" {
		input["accessLevel"] = q.AccessLevel
	}
	if q.RequestID != "" {
		input["requestId"] = q.RequestID
	}
	if q.ChatID != "" {
		input["chatId"] = q.ChatID
	}
	if q.Hidden != nil {
		input["hidden"] = *q.Hidden
	}
	if len(q.References) > 0 {
		input["references"] = q.References
	}
	if len(q.Params) > 0 {
		input["params"] = q.Params
	}
	if q.Scene != nil {
		input["scene"] = q.Scene
	}
	return input
}

func cloneScene(src *api.Scene) *api.Scene {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}
