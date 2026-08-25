package automation

import (
	"context"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
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
	TeamID        string
	Environment   Environment
	Query         Query
	SourceFile    string
}

type Execution struct {
	ID             string
	AutomationID   string
	AutomationName string
	SourceFile     string
	AgentKey       string
	TeamID         string
	ZoneID         string
	QueryContent   string
	ChatID         string
	RunID          string
	Status         string
	FinishReason   string
	ResultContent  string
	ResultPreview  string
	Error          string
	StartedAt      int64
	RunStartedAt   *int64
	CompletedAt    *int64
	DurationMs     *int64
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
	RequestID  string
	ChatID     string
	Role       string
	Hidden     *bool
	Message    string
	References []api.Reference
	Params     map[string]any
	Scene      *api.Scene
}

func (d Definition) ToQueryRequest() api.QueryRequest {
	params := contracts.CloneMap(d.Query.Params)
	hidden := EffectiveQueryHidden(d.Query.Hidden)
	chatSource := ""
	if id := strings.TrimSpace(d.ID); id != "" {
		chatSource = api.ChatSourceAutomationPrefix + id
	}

	return api.QueryRequest{
		RequestID:  d.Query.RequestID,
		ChatID:     d.Query.ChatID,
		AgentKey:   d.AgentKey,
		TeamID:     d.TeamID,
		Role:       EffectiveQueryRole(d.Query.Role),
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

func EffectiveQueryRole(role string) string {
	if strings.TrimSpace(role) == "" {
		return api.QueryRoleAutomation
	}
	normalized, ok := api.NormalizeQueryRole(role)
	if ok {
		return normalized
	}
	return strings.TrimSpace(role)
}

func cloneScene(src *api.Scene) *api.Scene {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}
