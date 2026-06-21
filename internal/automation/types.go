package automation

import (
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
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
	Status         string
	Error          string
	StartedAt      int64
	CompletedAt    *int64
	DurationMs     *int64
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
	Message    string
	References []api.Reference
	Params     map[string]any
	Scene      *api.Scene
}

func (d Definition) ToQueryRequest() api.QueryRequest {
	params := contracts.CloneMap(d.Query.Params)

	return api.QueryRequest{
		RequestID:  d.Query.RequestID,
		ChatID:     d.Query.ChatID,
		AgentKey:   d.AgentKey,
		TeamID:     d.TeamID,
		Role:       EffectiveQueryRole(d.Query.Role),
		Message:    d.Query.Message,
		References: append([]api.Reference(nil), d.Query.References...),
		Params:     params,
		Scene:      cloneScene(d.Query.Scene),
	}
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
