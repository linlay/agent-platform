package tools

import (
	"fmt"
	"regexp"
	"strings"
)

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
			return nil, fmt.Errorf("%w: %s", ErrToolArgsTemplateMissingValue, path)
		}
		next, ok := asMap[segment]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrToolArgsTemplateMissingValue, path)
		}
		current = next
	}
	return current, nil
}
