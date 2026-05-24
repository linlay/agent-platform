package llm

import (
	"context"

	"agent-platform/internal/chat"
)

func WithApprovalSummarySink(ctx context.Context, sink func(chat.StepApproval)) context.Context {
	return chat.WithApprovalSummarySink(ctx, sink)
}

func approvalSummarySinkFromContext(ctx context.Context) func(chat.StepApproval) {
	return chat.ApprovalSummarySinkFromContext(ctx)
}
