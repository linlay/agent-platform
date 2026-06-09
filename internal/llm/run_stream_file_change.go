package llm

import (
	"os"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func (s *llmRunStream) estimatedToolFileChange(invocation *preparedToolInvocation) map[string]any {
	if s == nil || invocation == nil || !isWriteTool(invocation.toolName) {
		return nil
	}
	if accessPlan := s.lookupFileAccessPlan(invocation); accessPlan != nil {
		if accessPlan.Blocked || s.fileAccessPlanNeedsApproval(*accessPlan) {
			return nil
		}
	}
	plan := s.lookupFileWritePlan(invocation)
	if plan == nil {
		return nil
	}

	beforeContent := ""
	beforeExists := false
	data, err := os.ReadFile(plan.FilePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil
		}
	} else {
		if !utf8.Valid(data) {
			return nil
		}
		beforeContent = string(data)
		beforeExists = true
	}

	switch strings.ToLower(strings.TrimSpace(plan.Operation)) {
	case "write":
		stats := contracts.ComputeLineDiffStats(beforeContent, string(plan.Content))
		return fileChangePayload(plan.FilePath, "write", stats)
	case "edit":
		afterContent, ok := estimateEditedContent(beforeContent, beforeExists, *plan)
		if !ok {
			return nil
		}
		if len([]byte(afterContent)) > maxInt(s.sessionFileToolsConfig(filetools.WriteAccess).MaxWriteBytes, 1<<20) {
			return nil
		}
		stats := contracts.ComputeLineDiffStats(beforeContent, afterContent)
		return fileChangePayload(plan.FilePath, "edit", stats)
	default:
		return nil
	}
}

func estimateEditedContent(beforeContent string, beforeExists bool, plan filetools.WritePlan) (string, bool) {
	normalizedContent := normalizeEstimatedEditString(beforeContent)
	oldString := normalizeEstimatedEditString(plan.OldString)
	newString := normalizeEstimatedEditString(plan.NewString)

	if oldString == "" {
		if beforeExists && normalizedContent != "" {
			return "", false
		}
		return newString, true
	}
	if !beforeExists {
		return "", false
	}
	replacements := strings.Count(normalizedContent, oldString)
	if replacements == 0 {
		return "", false
	}
	if replacements > 1 && !plan.ReplaceAll {
		return "", false
	}
	if plan.ReplaceAll {
		return strings.ReplaceAll(normalizedContent, oldString, newString), true
	}
	return strings.Replace(normalizedContent, oldString, newString, 1), true
}

func normalizeEstimatedEditString(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	return strings.ReplaceAll(content, "\r", "\n")
}

func toolResultFileChange(toolName string, result contracts.ToolExecutionResult) map[string]any {
	if !isWriteTool(toolName) || result.Error != "" || result.ExitCode != 0 || len(result.Structured) == 0 {
		return nil
	}
	filePath := strings.TrimSpace(contracts.AnyStringNode(result.Structured["filePath"]))
	if filePath == "" {
		return nil
	}
	lineStats := lineStatsFromResult(result.Structured["lineStats"])
	if len(lineStats) == 0 {
		return nil
	}
	operation := "write"
	if strings.EqualFold(strings.TrimSpace(toolName), "file_edit") {
		operation = "edit"
	}
	return map[string]any{
		"filePath":  filePath,
		"operation": operation,
		"lineStats": lineStats,
	}
}

func lineStatsFromResult(raw any) map[string]any {
	stats := contracts.AnyMapNode(raw)
	if len(stats) == 0 {
		return nil
	}
	out := map[string]any{
		"addedLines":   contracts.AnyIntNode(stats["addedLines"]),
		"deletedLines": contracts.AnyIntNode(stats["deletedLines"]),
		"editedLines":  contracts.AnyIntNode(stats["editedLines"]),
	}
	if _, ok := stats["addedLines"]; !ok {
		return nil
	}
	if _, ok := stats["deletedLines"]; !ok {
		return nil
	}
	if _, ok := stats["editedLines"]; !ok {
		return nil
	}
	return out
}

func fileChangePayload(filePath string, operation string, stats contracts.LineDiffStats) map[string]any {
	filePath = strings.TrimSpace(filePath)
	operation = strings.TrimSpace(operation)
	if filePath == "" || operation == "" {
		return nil
	}
	return map[string]any{
		"filePath":  filePath,
		"operation": operation,
		"lineStats": contracts.LineStatsPayload(stats),
	}
}
