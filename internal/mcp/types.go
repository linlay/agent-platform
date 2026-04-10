package mcp

import (
	"encoding/json"
	"strings"

	"agent-platform-runner-go/internal/api"
)

type ServerDefinition struct {
	Key              string
	Name             string
	BaseURL          string
	EndpointPath     string
	ToolPrefix       string
	AuthToken        string
	Headers          map[string]string
	AliasMap         map[string]string
	ConnectTimeoutMs int
	ReadTimeoutMs    int
	Retry            int
	Tools            []ToolDefinition
}

func (s ServerDefinition) ResolvedURL() string {
	base := strings.TrimRight(s.BaseURL, "/")
	path := strings.TrimLeft(s.EndpointPath, "/")
	if path == "" {
		return base
	}
	return base + "/" + path
}

type ToolDefinition struct {
	Key           string
	Name          string
	Label         string
	Description   string
	AfterCallHint string
	Parameters    map[string]any
	ToolAction    bool
	ToolType      string
	ViewportKey   string
	Aliases       []string
	Meta          map[string]any
}

func (t *ToolDefinition) UnmarshalJSON(data []byte) error {
	type rawToolDefinition struct {
		Key           string         `json:"key"`
		Name          string         `json:"name"`
		Label         string         `json:"label"`
		Description   string         `json:"description"`
		AfterCallHint string         `json:"afterCallHint"`
		InputSchema   map[string]any `json:"inputSchema"`
		Parameters    map[string]any `json:"parameters"`
		ToolAction    bool           `json:"toolAction"`
		ToolType      string         `json:"toolType"`
		ViewportKey   string         `json:"viewportKey"`
		Aliases       []string       `json:"aliases"`
		Meta          map[string]any `json:"meta"`
	}
	var raw rawToolDefinition
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parameters := raw.InputSchema
	if len(parameters) == 0 {
		parameters = raw.Parameters
	}
	*t = ToolDefinition{
		Key:           raw.Key,
		Name:          raw.Name,
		Label:         raw.Label,
		Description:   raw.Description,
		AfterCallHint: raw.AfterCallHint,
		Parameters:    cloneMap(parameters),
		ToolAction:    raw.ToolAction,
		ToolType:      raw.ToolType,
		ViewportKey:   raw.ViewportKey,
		Aliases:       append([]string(nil), raw.Aliases...),
		Meta:          cloneMap(raw.Meta),
	}
	return nil
}

type JSONRPCRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      string         `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      string        `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (t ToolDefinition) ToAPITool(serverKey string) api.ToolDetailResponse {
	kind := "backend"
	if t.ToolAction {
		kind = "action"
	} else if strings.TrimSpace(t.ToolType) != "" || strings.TrimSpace(t.ViewportKey) != "" {
		kind = "frontend"
	}
	meta := map[string]any{
		"kind":         kind,
		"serverKey":    serverKey,
		"sourceType":   "mcp",
		"sourceKey":    serverKey,
		"toolAction":   t.ToolAction,
		"clientVisible": true,
	}
	if strings.TrimSpace(t.ToolType) != "" {
		meta["toolType"] = strings.TrimSpace(t.ToolType)
	}
	if strings.TrimSpace(t.ViewportKey) != "" {
		meta["viewportKey"] = strings.TrimSpace(t.ViewportKey)
	}
	for key, value := range t.Meta {
		meta[key] = value
	}
	return api.ToolDetailResponse{
		Key:           defaultToolKey(t.Key, t.Name),
		Name:          t.Name,
		Label:         t.Label,
		Description:   t.Description,
		AfterCallHint: t.AfterCallHint,
		Parameters:    cloneMap(t.Parameters),
		Meta:          meta,
	}
}

func defaultToolKey(key string, name string) string {
	if strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	return strings.TrimSpace(name)
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
