package schedule

import (
	"time"

	"agent-platform-runner-go/internal/api"
)

type Definition struct {
	ID           string
	Name         string
	Description  string
	Enabled      bool
	Cron         string
	AgentKey     string
	TeamID       string
	Environment  Environment
	Query        Query
	PushURL      string
	PushTargetID string
	SourceFile   string
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
	params := cloneMap(d.Query.Params)
	if params == nil {
		params = map[string]any{}
	}
	params["__schedule"] = map[string]any{
		"scheduleId":          d.ID,
		"scheduleName":        d.Name,
		"scheduleDescription": d.Description,
		"sourceFile":          d.SourceFile,
		"triggeredAt":         time.Now().UnixMilli(),
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

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
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
