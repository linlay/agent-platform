package chat

import "context"

type approvalSummaryContextKey struct{}

func WithApprovalSummarySink(ctx context.Context, sink func(StepApproval)) context.Context {
	if sink == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, approvalSummaryContextKey{}, sink)
}

func ApprovalSummarySinkFromContext(ctx context.Context) func(StepApproval) {
	if ctx == nil {
		return nil
	}
	sink, _ := ctx.Value(approvalSummaryContextKey{}).(func(StepApproval))
	return sink
}
