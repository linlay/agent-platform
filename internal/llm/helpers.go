package llm

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/contracts"
)

func runTimeout(b contracts.Budget) time.Duration {
	return time.Duration(maxInt(b.RunTimeoutMs, 1)) * time.Millisecond
}

func toolTimeout(policy contracts.RetryPolicy) time.Duration {
	return time.Duration(maxInt(policy.TimeoutMs, 1)) * time.Millisecond
}

func structuredOrOutput(result contracts.ToolExecutionResult) any {
	if len(result.Structured) > 0 {
		return result.Structured
	}
	return result.Output
}

func sseResultValue(result contracts.ToolExecutionResult) any {
	if result.RawParams != nil {
		return result.RawParams
	}
	if result.Error != "" {
		return result.Output
	}
	return structuredOrOutput(result)
}

func formatToolErrorOutput(code string, message string) string {
	code = strings.TrimSpace(code)
	message = strings.TrimSpace(message)
	switch {
	case code == "":
		return message
	case message == "":
		return code
	default:
		return code + ": " + message
	}
}

func maxInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func cloneToolDefinition(def api.ToolDetailResponse) api.ToolDetailResponse {
	return api.ToolDetailResponse{
		Key:           def.Key,
		Name:          def.Name,
		Label:         def.Label,
		Description:   def.Description,
		AfterCallHint: def.AfterCallHint,
		Parameters:    contracts.CloneMap(def.Parameters),
		Meta:          contracts.CloneMap(def.Meta),
	}
}

func defaultEndpointPath(protocol string, baseURL string) string {
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "ANTHROPIC":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/messages"
		}
		return "/v1/messages"
	case "", "OPENAI":
		if normalizedBasePath(baseURL) == "/v1" {
			return "/chat/completions"
		}
		return "/v1/chat/completions"
	default:
		return ""
	}
}

func normalizedBasePath(rawBaseURL string) string {
	parsed, err := urlParse(strings.TrimSpace(rawBaseURL))
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(parsed.EscapedPath())
	if path == "" {
		path = strings.TrimSpace(parsed.Path)
	}
	if path == "" || path == "/" {
		return ""
	}
	return "/" + strings.Trim(strings.TrimSpace(path), "/")
}

var previousResultPattern = regexp.MustCompile(`\$\{previousResult\.([a-zA-Z0-9_.-]+)\}`)

func ExpandToolArgsTemplates(input any, previousResult any) (any, error) {
	switch value := input.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			expanded, err := ExpandToolArgsTemplates(item, previousResult)
			if err != nil {
				return nil, err
			}
			out[key] = expanded
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(value))
		for _, item := range value {
			expanded, err := ExpandToolArgsTemplates(item, previousResult)
			if err != nil {
				return nil, err
			}
			out = append(out, expanded)
		}
		return out, nil
	case string:
		return expandTemplateString(value, previousResult)
	default:
		return input, nil
	}
}

func expandTemplateString(value string, previousResult any) (any, error) {
	matches := previousResultPattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value, nil
	}
	if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(value) {
		resolved, err := resolvePreviousResultPath(value[matches[0][2]:matches[0][3]], previousResult)
		if err != nil {
			return nil, err
		}
		return resolved, nil
	}

	var builder strings.Builder
	last := 0
	for _, match := range matches {
		builder.WriteString(value[last:match[0]])
		resolved, err := resolvePreviousResultPath(value[match[2]:match[3]], previousResult)
		if err != nil {
			return nil, err
		}
		builder.WriteString(fmt.Sprint(resolved))
		last = match[1]
	}
	builder.WriteString(value[last:])
	return builder.String(), nil
}

func resolvePreviousResultPath(path string, previousResult any) (any, error) {
	current := previousResult
	for _, segment := range strings.Split(strings.TrimSpace(path), ".") {
		if segment == "" {
			continue
		}
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: %s", contracts.ErrToolArgsTemplateMissingValue, path)
		}
		next, ok := asMap[segment]
		if !ok {
			return nil, fmt.Errorf("%w: %s", contracts.ErrToolArgsTemplateMissingValue, path)
		}
		current = next
	}
	return current, nil
}

func urlParse(raw string) (*url.URL, error) {
	return url.Parse(raw)
}
