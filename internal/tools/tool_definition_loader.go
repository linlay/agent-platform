package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

type toolDefinitionParseOptions struct {
	sourceType       string
	sourceCategory   string
	defaultSourceKey string
	baseDir          string
	defaultExternal  map[string]any
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
	outputSchema := AnyMapNode(root["outputSchema"])
	typeValue := strings.ToLower(AnyStringNode(root["type"]))
	viewportType := AnyStringNode(root["viewportType"])
	viewportKey := AnyStringNode(root["viewportKey"])
	kind := "backend"
	external := AnyMapNode(root["external"])
	if len(external) == 0 && len(options.defaultExternal) > 0 {
		external = CloneMap(options.defaultExternal)
	}
	switch typeValue {
	case "frontend":
		kind = "frontend"
	case "action":
		kind = "action"
	case "external":
		kind = "external"
	case "backend", "builtin", "function", "":
		kind = "backend"
	default:
		kind = "backend"
	}
	if len(external) > 0 {
		kind = "external"
	} else if AnyBoolNode(root["toolAction"]) {
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
	sourceCategory := strings.ToLower(strings.TrimSpace(options.sourceCategory))
	if sourceCategory == "" {
		switch {
		case strings.EqualFold(sourceType, "mcp"):
			sourceCategory = "mcp"
		case strings.EqualFold(sourceType, "agent-local"):
			sourceCategory = "external"
		case strings.EqualFold(kind, "external"):
			sourceCategory = "external"
		default:
			sourceCategory = "platform"
		}
	}

	meta := map[string]any{
		"kind":           kind,
		"sourceType":     sourceType,
		"sourceCategory": sourceCategory,
	}
	if strict, ok := root["strict"].(bool); ok {
		meta["strict"] = strict
	}
	if clientVisible, ok := root["clientVisible"].(bool); ok {
		meta["clientVisible"] = clientVisible
	}
	if explicitOnly, ok := root["explicitOnly"].(bool); ok {
		meta["explicitOnly"] = explicitOnly
	}
	if internalOnly, ok := root["internalOnly"].(bool); ok {
		meta["internalOnly"] = internalOnly
	}
	if catalogVisible, ok := root["catalogVisible"].(bool); ok {
		meta["catalogVisible"] = catalogVisible
	}
	if readOnly, ok := root["readOnly"].(bool); ok {
		meta["readOnly"] = readOnly
	}
	tags, err := publicToolTags(root["tags"])
	if err != nil {
		return api.ToolDetailResponse{}, err
	}
	if len(tags) > 0 {
		meta["tags"] = tags
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
	if timeout := AnyIntNode(root["timeout"]); timeout > 0 {
		meta["timeout"] = timeout
	}
	if len(external) > 0 {
		externalMeta, err := normalizeExternalToolMeta(name, sourceKey, external, options.baseDir)
		if err != nil {
			return api.ToolDetailResponse{}, err
		}
		meta["external"] = externalMeta
		if serviceKey := AnyStringNode(externalMeta["serviceKey"]); serviceKey != "" {
			meta["serviceKey"] = serviceKey
		}
	}
	if sourceKey != "" {
		meta["sourceKey"] = sourceKey
	}
	if submitResultFormat := AnyStringNode(root["submitResultFormat"]); submitResultFormat != "" {
		meta["submitResultFormat"] = submitResultFormat
	}
	return api.ToolDetailResponse{
		Key:           fallbackToolString(AnyStringNode(root["key"]), name),
		Name:          name,
		Label:         AnyStringNode(root["label"]),
		Description:   AnyStringNode(root["description"]),
		AfterCallHint: AnyStringNode(root["afterCallHint"]),
		Parameters:    CloneMap(parameters),
		OutputSchema:  CloneMap(outputSchema),
		Meta:          meta,
	}, nil
}

func normalizeExternalToolMeta(toolName string, sourceKey string, external map[string]any, baseDir string) (map[string]any, error) {
	out := CloneMap(external)
	transport := strings.TrimSpace(AnyStringNode(out["transport"]))
	if transport == "" {
		transport = "stdio-jsonrpc"
		out["transport"] = transport
	}
	if !strings.EqualFold(transport, "stdio-jsonrpc") {
		return nil, fmt.Errorf("external.transport for tool %q must be stdio-jsonrpc", toolName)
	}
	serviceKey := strings.TrimSpace(AnyStringNode(out["serviceKey"]))
	if serviceKey == "" {
		serviceKey = strings.TrimSpace(sourceKey)
	}
	if serviceKey == "" {
		serviceKey = strings.TrimSpace(toolName)
	}
	out["serviceKey"] = serviceKey
	command := strings.TrimSpace(AnyStringNode(out["command"]))
	if command == "" {
		return nil, fmt.Errorf("external.command is required for tool %q", toolName)
	}
	baseDir = strings.TrimSpace(baseDir)
	if baseDir != "" && !filepath.IsAbs(command) {
		command = filepath.Join(baseDir, command)
		out["command"] = command
	}
	if strings.TrimSpace(AnyStringNode(out["workingDirectory"])) == "" && baseDir != "" {
		out["workingDirectory"] = baseDir
	}
	return out, nil
}

func fallbackToolString(value string, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}

func publicToolTags(value any) ([]string, error) {
	var raw []string
	switch typed := value.(type) {
	case nil:
	case []string:
		raw = append(raw, typed...)
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("tags must contain only strings")
			}
			raw = append(raw, text)
		}
	case string:
		raw = append(raw, splitPublicToolTagText(typed)...)
	default:
		return nil, fmt.Errorf("tags must be a string or list of strings")
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out, nil
}

func splitPublicToolTagText(value string) []string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSpace(value[1 : len(value)-1])
		parts := strings.Split(value, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.Trim(strings.TrimSpace(part), `"'`)
			if part != "" {
				out = append(out, part)
			}
		}
		return out
	}
	return []string{value}
}
