package mcp

import "agent-platform-runner-go/internal/api"

type ServerDefinition struct {
	Key       string
	Name      string
	BaseURL   string
	AuthToken string
	TimeoutMs int
	Tools     []ToolDefinition
}

type ToolDefinition struct {
	Key         string
	Name        string
	Description string
	Parameters  map[string]any
	Meta        map[string]any
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
	meta := map[string]any{
		"kind":      "mcp",
		"serverKey": serverKey,
	}
	for key, value := range t.Meta {
		meta[key] = value
	}
	return api.ToolDetailResponse{
		Key:         t.Key,
		Name:        t.Name,
		Description: t.Description,
		Parameters:  cloneMap(t.Parameters),
		Meta:        meta,
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
