package llm

import (
	"strings"

	"agent-platform/internal/api"
	. "agent-platform/internal/contracts"
)

func resolveFrontendAwaitTimeout(toolName string, tool api.ToolDetailResponse, args map[string]any, budget Budget) int64 {
	mode := strings.ToLower(strings.TrimSpace(AnyStringNode(args["mode"])))
	if mode == "" {
		if strings.EqualFold(strings.TrimSpace(toolName), "ask_user_question") {
			mode = "question"
		} else {
			mode = "form"
		}
	}
	itemTimeoutMs := int64(0)
	if mode == "question" {
		itemTimeoutMs = int64(AnyIntNode(args["timeoutMs"]))
	}
	if mode == "form" {
		itemTimeoutMs = int64(AnyIntNode(tool.Meta["timeoutMs"]))
	}
	return ResolveHITLTimeout(mode, itemTimeoutMs, budget)
}
