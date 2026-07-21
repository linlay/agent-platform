package kbase

import (
	"context"

	"agent-platform/internal/supportpkg"
)

type lanceRuntime struct {
	engine *LanceEngineProcess
	store  *LanceRetrievalStore
}

func newLanceRuntime() *lanceRuntime {
	engine := NewLanceEngineProcess(nil)
	return &lanceRuntime{engine: engine, store: NewLanceRetrievalStore(engine)}
}

func (r *lanceRuntime) SetSupportPackages(registry *supportpkg.Registry) {
	if r != nil && r.engine != nil {
		r.engine.SetRegistry(registry)
	}
}

func (r *lanceRuntime) SetLifecycleContext(ctx context.Context) {
	if r != nil && r.engine != nil {
		r.engine.SetLifecycleContext(ctx)
	}
}

func (r *lanceRuntime) Probe(ctx context.Context, required bool) (bool, LanceEngineState, error) {
	if r == nil || r.engine == nil {
		return required, LanceEngineState{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.engine.EnsureStarted(ctx); err != nil {
		return required, r.engine.State(), err
	}
	return required, r.engine.State(), nil
}

func (r *lanceRuntime) State() LanceEngineState {
	if r == nil || r.engine == nil {
		return LanceEngineState{}
	}
	return r.engine.State()
}

func (r *lanceRuntime) Stop(ctx context.Context) error {
	if r == nil || r.engine == nil {
		return nil
	}
	return r.engine.Stop(ctx)
}
