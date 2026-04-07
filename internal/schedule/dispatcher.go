package schedule

import (
	"context"

	"agent-platform-runner-go/internal/api"
)

type DispatchFunc func(ctx context.Context, req api.QueryRequest) error

type Dispatcher struct {
	dispatch DispatchFunc
}

func NewDispatcher(dispatch DispatchFunc) *Dispatcher {
	return &Dispatcher{dispatch: dispatch}
}

func (d *Dispatcher) Dispatch(ctx context.Context, def Definition) error {
	if d == nil || d.dispatch == nil {
		return nil
	}
	if !def.Enabled {
		return nil
	}
	return d.dispatch(ctx, def.ToQueryRequest())
}
