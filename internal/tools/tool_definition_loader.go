package tools

import (
	"fmt"
	"strings"

	"agent-platform-runner-go/internal/api"
	. "agent-platform-runner-go/internal/contracts"
)

type toolDefinitionParseOptions struct {
	sourceType       string
	defaultSourceKey string
}

func parseToolDefinition(root map[string]any, options toolDefinitionParseOptions) (api.ToolDetailResponse, error) {
	name := AnyStringNode(root["name"])
	if name == "" {
		return api.ToolDetailResponse{}, fmt.Errorf("name is required")
	}
	parameters := AnyMapNode(root["inputSchema"])
	if len(parameters) == 0 {
		parameters = AnyMapNode(root["parameters"])
	}
	typeValue := strings.ToLower(AnyStringNode(root["type"]))
	viewportType := firstNonEmptyStringNode(root["viewportType"], root["toolType"])
	viewportKey := AnyStringNode(root["viewportKey"])
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
	if AnyBoolNode(root["toolAction"]) {
		kind = "action"
	} else if viewportType != "" || viewportKey != "" {
		kind = "frontend"
	}

	sourceType := strings.TrimSpace(options.sourceType)
	if sourceType == "" {
		sourceType = "agent-local"
	}
	sourceKey := AnyStringNode(root["sourceKey"])
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
	if viewportType != "" {
		meta["viewportType"] = viewportType
	}
	if viewportKey != "" {
		meta["viewportKey"] = viewportKey
	}
	if sourceKey != "" {
		meta["sourceKey"] = sourceKey
	}
	return api.ToolDetailResponse{
		Key:           fallbackToolString(AnyStringNode(root["key"]), name),
		Name:          name,
		Label:         AnyStringNode(root["label"]),
		Description:   AnyStringNode(root["description"]),
		AfterCallHint: AnyStringNode(root["afterCallHint"]),
		Parameters:    CloneMap(parameters),
		Meta:          meta,
	}, nil
}

func fallbackToolString(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}

func firstNonEmptyStringNode(values ...any) string {
	for _, value := range values {
		if text := AnyStringNode(value); text != "" {
			return text
		}
	}
	return ""
}
