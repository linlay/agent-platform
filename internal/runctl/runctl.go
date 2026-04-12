package runctl

import (
	"context"

	"agent-platform-runner-go/internal/contracts"
)

type RunControl = contracts.RunControl
type SubmitResult = contracts.SubmitResult
type InMemoryRunManager = contracts.InMemoryRunManager

func NewRunControl(parent context.Context, runID string) *RunControl {
	return contracts.NewRunControl(parent, runID)
}

func WithRunControl(ctx context.Context, control *RunControl) context.Context {
	return contracts.WithRunControl(ctx, control)
}

func RunControlFromContext(ctx context.Context) *RunControl {
	return contracts.RunControlFromContext(ctx)
}

func NewInMemoryRunManager() *InMemoryRunManager {
	return contracts.NewInMemoryRunManager()
}

func IsRunInterrupted(err error) bool {
	return contracts.IsRunInterrupted(err)
}
