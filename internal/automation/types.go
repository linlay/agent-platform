package automation

import (
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
	PushURL       string
	PushTargetID  string
	PushMessage   string
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
	Hidden     *bool
}

func (d Definition) ToQueryRequest() api.QueryRequest {
	params := contracts.CloneMap(d.Query.Params)
	if params == nil {
		params = map[string]any{}
	}
	params["__automation"] = map[string]any{
		"automationId":          d.ID,
		"automationName":        d.Name,
		"automationDescription": d.Description,
		"sourceFile":            d.SourceFile,
		"triggeredAt":           time.Now().UnixMilli(),
	}

	role := d.Query.Role
	if role == "" {
		role = "user"
	}

	return api.QueryRequest{
		RequestID:  d.Query.RequestID,
		ChatID:     d.Query.ChatID,
		AgentKey:   d.AgentKey,
		TeamID:     d.TeamID,
		Role:       role,
		Message:    d.Query.Message,
		References: append([]api.Reference(nil), d.Query.References...),
		Params:     params,
		Scene:      cloneScene(d.Query.Scene),
		Hidden:     cloneBoolPtr(d.Query.Hidden),
	}
}

func cloneScene(src *api.Scene) *api.Scene {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

func cloneBoolPtr(src *bool) *bool {
	if src == nil {
		return nil
	}
	value := *src
	return &value
}
