package tools

import (
	"regexp"
	"strings"

	. "agent-platform/internal/contracts"
)

const defaultRegexPreviewLimit = 100

func (t *RuntimeToolExecutor) invokeRegex(args map[string]any) ToolExecutionResult {
	operation := strings.ToLower(strings.TrimSpace(stringArg(args, "operation")))
	if operation != "count" && operation != "matches" {
		return regexErrorResult("regex_invalid_operation", "operation must be count or matches")
	}
	pattern := stringArg(args, "pattern")
	if strings.TrimSpace(pattern) == "" {
		return regexErrorResult("regex_invalid_pattern", "pattern is required")
	}
	compiledPattern := pattern
	if boolArg(args, "caseInsensitive") {
		compiledPattern = "(?i)" + compiledPattern
	}
	re, err := regexp.Compile(compiledPattern)
	if err != nil {
		return regexErrorResult("regex_invalid_pattern", err.Error())
	}

	text := stringArg(args, "text")
	limit := defaultRegexPreviewLimit
	if _, ok := args["limit"]; ok {
		limit = numericArg(args, "limit")
		if limit < 0 {
			limit = 0
		}
	}

	indexes := re.FindAllStringSubmatchIndex(text, -1)
	count := len(indexes)
	previewLimit := limit
	if previewLimit > count {
		previewLimit = count
	}
	matches := make([]map[string]any, 0, previewLimit)
	for _, item := range indexes[:previewLimit] {
		matches = append(matches, regexMatchPayload(text, item))
	}

	return structuredResult(map[string]any{
		"tool":      "regex",
		"operation": operation,
		"pattern":   pattern,
		"count":     count,
		"matches":   matches,
		"truncated": count > previewLimit,
	})
}

func regexMatchPayload(text string, indexes []int) map[string]any {
	match := map[string]any{
		"text":  text[indexes[0]:indexes[1]],
		"start": indexes[0],
		"end":   indexes[1],
	}
	groups := make([]any, 0, len(indexes)/2-1)
	for i := 2; i+1 < len(indexes); i += 2 {
		if indexes[i] < 0 || indexes[i+1] < 0 {
			groups = append(groups, nil)
			continue
		}
		groups = append(groups, text[indexes[i]:indexes[i+1]])
	}
	match["groups"] = groups
	return match
}

func regexErrorResult(code string, message string) ToolExecutionResult {
	result := structuredResultWithExit(map[string]any{
		"tool":    "regex",
		"error":   code,
		"message": message,
	}, -1)
	result.Error = code
	return result
}
