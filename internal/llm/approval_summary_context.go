package llm

import (
	"context"

	"agent-platform-runner-go/internal/chat"
)

type approvalSummaryContextKey struct{}

func WithApprovalSummarySink(ctx context.Context, sink func(chat.StepApproval)) context.Context {
	if sink == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, approvalSummaryContextKey{}, sink)
}

func approvalSummarySinkFromContext(ctx context.Context) func(chat.StepApproval) {
	if ctx == nil {
		return nil
	}
	sink, _ := ctx.Value(approvalSummaryContextKey{}).(func(chat.StepApproval))
	return sink
}
