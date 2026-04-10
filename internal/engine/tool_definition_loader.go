package engine

import (
	"fmt"
	"strings"

	"agent-platform-runner-go/internal/api"
)

type toolDefinitionParseOptions struct {
	sourceType       string
	defaultSourceKey string
}

func parseToolDefinition(root map[string]any, options toolDefinitionParseOptions) (api.ToolDetailResponse, error) {
	name := anyStringNode(root["name"])
	if name == "" {
		return api.ToolDetailResponse{}, fmt.Errorf("name is required")
	}
	parameters := anyMapNode(root["inputSchema"])
	if len(parameters) == 0 {
		parameters = anyMapNode(root["parameters"])
	}
	typeValue := strings.ToLower(anyStringNode(root["type"]))
	toolType := anyStringNode(root["toolType"])
	viewportKey := anyStringNode(root["viewportKey"])
	kind := "backend"
	switch typeValue {
	case "frontend":
		kind = "frontend"
	case "action":
		kind = "action"
	case "backend", "builtin", "function", "":
		kind = "backend"
	default:
		kind = "backend"
	}
	if anyBoolNode(root["toolAction"]) {
		kind = "action"
	} else if toolType != "" || viewportKey != "" {
		kind = "frontend"
	}

	sourceType := strings.TrimSpace(options.sourceType)
	if sourceType == "" {
		sourceType = "agent-local"
	}
	sourceKey := anyStringNode(root["sourceKey"])
	if sourceKey == "" {
		sourceKey = strings.TrimSpace(options.defaultSourceKey)
	}
	if sourceKey == "" && !strings.EqualFold(sourceType, "mcp") {
		sourceKey = name
	}

	meta := map[string]any{
		"kind":       kind,
		"sourceType": sourceType,
	}
	if strict, ok := root["strict"].(bool); ok {
		meta["strict"] = strict
	}
	if clientVisible, ok := root["clientVisible"].(bool); ok {
		meta["clientVisible"] = clientVisible
	}
	if toolAction, ok := root["toolAction"].(bool); ok {
		meta["toolAction"] = toolAction
	}
	if toolType != "" {
		meta["toolType"] = toolType
	}
	if viewportKey != "" {
		meta["viewportKey"] = viewportKey
	}
	if sourceKey != "" {
		meta["sourceKey"] = sourceKey
	}
	return api.ToolDetailResponse{
		Key:           fallbackToolString(anyStringNode(root["key"]), name),
		Name:          name,
		Label:         anyStringNode(root["label"]),
		Description:   anyStringNode(root["description"]),
		AfterCallHint: anyStringNode(root["afterCallHint"]),
		Parameters:    cloneAnyMap(parameters),
		Meta:          meta,
	}, nil
}

func fallbackToolString(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}
