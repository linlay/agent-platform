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
	itemTimeout := int64(0)
	if mode == "form" {
		itemTimeout = int64(AnyIntNode(tool.Meta["timeout"]))
	}
	return ResolveHITLTimeout(mode, itemTimeout, budget)
}
