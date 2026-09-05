package llm

import (
	"context"

	"agent-platform/internal/chat"
)

func approvalSummarySinkFromContext(ctx context.Context) func(chat.StepApproval) {
	return chat.ApprovalSummarySinkFromContext(ctx)
}
