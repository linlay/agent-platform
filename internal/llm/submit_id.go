package llm

import (
	"strings"

	. "agent-platform/internal/contracts"
)

func awaitingAnswerWithSubmitID(answer map[string]any, submitID string) map[string]any {
	out := CloneMap(answer)
	if strings.TrimSpace(submitID) != "" {
		out["submitId"] = strings.TrimSpace(submitID)
	}
	return out
}
