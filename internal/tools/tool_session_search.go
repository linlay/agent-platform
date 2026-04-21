package tools

import (
	"strings"

	. "agent-platform-runner-go/internal/contracts"
)

func (t *RuntimeToolExecutor) invokeSessionSearch(toolName string, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if t.chats == nil {
		return ToolExecutionResult{Output: "chat store not configured", Error: "chat_store_not_configured", ExitCode: -1}, nil
	}
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return ToolExecutionResult{Output: "query must not be blank", Error: "missing_query", ExitCode: -1}, nil
	}
	chatID := strings.TrimSpace(stringArg(args, "chatId"))
	if chatID == "" && execCtx != nil {
		chatID = strings.TrimSpace(execCtx.Session.ChatID)
	}
	if chatID == "" {
		return ToolExecutionResult{Output: "chatId must not be blank", Error: "missing_chat_id", ExitCode: -1}, nil
	}
	limit := int(int64Arg(args, "limit"))
	if limit <= 0 {
		limit = 10
	}
	hits, err := t.chats.SearchSession(chatID, query, limit)
	if err != nil {
		return ToolExecutionResult{}, err
	}
	results := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		results = append(results, map[string]any{
			"kind":      hit.Kind,
			"chatId":    hit.ChatID,
			"runId":     hit.RunID,
			"stage":     hit.Stage,
			"role":      hit.Role,
			"timestamp": hit.Timestamp,
			"snippet":   hit.Snippet,
			"score":     hit.Score,
			"meta":      hit.Meta,
		})
	}
	return structuredResult(map[string]any{
		"chatId":  chatID,
		"query":   query,
		"count":   len(results),
		"results": results,
	}), nil
}
