package contracts

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform-runner-go/internal/api"
)

type runControlContextKey struct{}

type SubmitResult struct {
	Request api.SubmitRequest
	Status  string
	Detail  string
}

type submitWaiter struct {
	ch chan SubmitResult
}

func (w *submitWaiter) deliver(result SubmitResult) bool {
	if w == nil {
		return false
	}
	select {
	case w.ch <- result:
		return true
	default:
		return false
	}
}

type RunControl struct {
	runID string

	ctx    context.Context
	cancel context.CancelFunc

	interrupted atomic.Bool
	finished    atomic.Bool

	mu            sync.Mutex
	steerQueue    []api.SteerRequest
	submitWaiters map[string]*submitWaiter
	state         RunLoopState
}

func NewRunControl(parent context.Context, runID string) *RunControl {
	ctx, cancel := context.WithCancel(parent)
	return &RunControl{
		runID:         runID,
		ctx:           ctx,
		cancel:        cancel,
		submitWaiters: map[string]*submitWaiter{},
		state:         RunLoopStateIdle,
	}
}

func WithRunControl(ctx context.Context, control *RunControl) context.Context {
	if control == nil {
		return ctx
	}
	return context.WithValue(ctx, runControlContextKey{}, control)
}

func RunControlFromContext(ctx context.Context) *RunControl {
	if ctx == nil {
		return nil
	}
	control, _ := ctx.Value(runControlContextKey{}).(*RunControl)
	return control
}

func (c *RunControl) Context() context.Context {
	if c == nil {
		return context.Background()
	}
	return c.ctx
}

func (c *RunControl) Interrupted() bool {
	return c != nil && c.interrupted.Load()
}

func (c *RunControl) Finished() bool {
	return c != nil && c.finished.Load()
}

func (c *RunControl) Interrupt() bool {
	if c == nil {
		return false
	}
	if !c.interrupted.CompareAndSwap(false, true) {
		return false
	}
	c.TransitionState(RunLoopStateCancelled)
	c.cancel()
	c.closeWaiters("interrupted", "Run interrupted")
	return true
}

func (c *RunControl) Finish() bool {
	if c == nil {
		return false
	}
	if !c.finished.CompareAndSwap(false, true) {
		return false
	}
	c.TransitionState(RunLoopStateCompleted)
	c.cancel()
	c.closeWaiters("finished", "Run finished before submit arrived")
	return true
}

func (c *RunControl) EnqueueSteer(req api.SteerRequest) bool {
	if c == nil || c.interrupted.Load() || c.finished.Load() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.interrupted.Load() || c.finished.Load() {
		return false
	}
	c.steerQueue = append(c.steerQueue, req)
	return true
}

func (c *RunControl) DrainSteers() []api.SteerRequest {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.steerQueue) == 0 {
		return nil
	}
	queue := append([]api.SteerRequest(nil), c.steerQueue...)
	c.steerQueue = nil
	return queue
}

func (c *RunControl) AwaitSubmit(ctx context.Context, toolID string) (SubmitResult, error) {
	return c.AwaitSubmitWithTimeout(ctx, toolID, 0)
}

func (c *RunControl) AwaitSubmitWithTimeout(ctx context.Context, toolID string, timeout time.Duration) (SubmitResult, error) {
	if c == nil {
		return SubmitResult{}, ErrRunControlUnavailable
	}
	if toolID == "" {
		return SubmitResult{}, ErrFrontendToolMissingToolID
	}
	if c.interrupted.Load() {
		return SubmitResult{}, ErrRunInterrupted
	}
	if c.finished.Load() {
		return SubmitResult{}, ErrRunFinished
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	waiter := &submitWaiter{ch: make(chan SubmitResult, 1)}
	c.mu.Lock()
	if c.interrupted.Load() {
		c.mu.Unlock()
		return SubmitResult{}, ErrRunInterrupted
	}
	if c.finished.Load() {
		c.mu.Unlock()
		return SubmitResult{}, ErrRunFinished
	}
	if _, exists := c.submitWaiters[toolID]; exists {
		c.mu.Unlock()
		return SubmitResult{}, ErrFrontendSubmitAlreadyWaiting
	}
	c.submitWaiters[toolID] = waiter
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if current, exists := c.submitWaiters[toolID]; exists && current == waiter {
			delete(c.submitWaiters, toolID)
		}
		c.mu.Unlock()
	}()

	select {
	case result := <-waiter.ch:
		switch result.Status {
		case "interrupted":
			return SubmitResult{}, ErrRunInterrupted
		case "finished":
			return SubmitResult{}, ErrRunFinished
		default:
			return result, nil
		}
	case <-ctx.Done():
		return SubmitResult{}, ctx.Err()
	case <-c.ctx.Done():
		if c.interrupted.Load() {
			return SubmitResult{}, ErrRunInterrupted
		}
		if c.finished.Load() {
			return SubmitResult{}, ErrRunFinished
		}
		return SubmitResult{}, context.Canceled
	}
}

func (c *RunControl) State() RunLoopState {
	if c == nil {
		return RunLoopStateIdle
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *RunControl) TransitionState(next RunLoopState) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.state = next
	c.mu.Unlock()
}

func (c *RunControl) ResolveSubmit(req api.SubmitRequest) SubmitAck {
	if c == nil {
		return SubmitAck{Accepted: false, Status: "unmatched", Detail: "No active run found"}
	}
	c.mu.Lock()
	waiter, ok := c.submitWaiters[req.ToolID]
	if ok {
		delete(c.submitWaiters, req.ToolID)
	}
	c.mu.Unlock()
	if !ok {
		return SubmitAck{Accepted: false, Status: "unmatched", Detail: "No pending frontend submit waiter found"}
	}
	if !waiter.deliver(SubmitResult{
		Request: req,
		Status:  "accepted",
		Detail:  "Frontend submit accepted",
	}) {
		return SubmitAck{Accepted: false, Status: "unmatched", Detail: "Frontend submit waiter is no longer active"}
	}
	return SubmitAck{Accepted: true, Status: "accepted", Detail: "Frontend submit accepted"}
}

func (c *RunControl) closeWaiters(status string, detail string) {
	c.mu.Lock()
	waiters := c.submitWaiters
	c.submitWaiters = map[string]*submitWaiter{}
	c.mu.Unlock()
	for _, waiter := range waiters {
		waiter.deliver(SubmitResult{Status: status, Detail: detail})
	}
}

type activeRunState struct {
	run     ActiveRun
	control *RunControl
}

type InMemoryRunManager struct {
	mu   sync.Mutex
	runs map[string]activeRunState
}

func NewInMemoryRunManager() *InMemoryRunManager {
	return &InMemoryRunManager{runs: map[string]activeRunState{}}
}

func (m *InMemoryRunManager) Register(parent context.Context, session QuerySession) (context.Context, *RunControl, ActiveRun) {
	m.mu.Lock()
	defer m.mu.Unlock()

	control := NewRunControl(parent, session.RunID)
	run := ActiveRun{RunID: session.RunID, ChatID: session.ChatID, AgentKey: session.AgentKey}
	m.runs[session.RunID] = activeRunState{run: run, control: control}
	return WithRunControl(control.Context(), control), control, run
}

func (m *InMemoryRunManager) Submit(req api.SubmitRequest) SubmitAck {
	control, ok := m.lookupControl(req.RunID)
	if !ok {
		return SubmitAck{Accepted: false, Status: "unmatched", Detail: "No active run found"}
	}
	return control.ResolveSubmit(req)
}

func (m *InMemoryRunManager) Steer(req api.SteerRequest) SteerAck {
	control, ok := m.lookupControl(req.RunID)
	steerID := normalizeSteerID(req.SteerID)
	if !ok {
		return SteerAck{Accepted: false, Status: "unmatched", SteerID: steerID, Detail: "No active run found"}
	}
	req.SteerID = steerID
	if !control.EnqueueSteer(req) {
		return SteerAck{Accepted: false, Status: "unmatched", SteerID: steerID, Detail: "Run is no longer accepting steer"}
	}
	return SteerAck{Accepted: true, Status: "accepted", SteerID: steerID, Detail: "Steer accepted"}
}

func (m *InMemoryRunManager) Interrupt(req api.InterruptRequest) InterruptAck {
	m.mu.Lock()
	state, ok := m.runs[req.RunID]
	if ok {
		delete(m.runs, req.RunID)
	}
	m.mu.Unlock()
	if !ok {
		return InterruptAck{Accepted: false, Status: "unmatched", Detail: "No active run found"}
	}
	state.control.Interrupt()
	return InterruptAck{Accepted: true, Status: "accepted", Detail: "Interrupt accepted"}
}

func (m *InMemoryRunManager) Finish(runID string) {
	m.mu.Lock()
	state, ok := m.runs[runID]
	if ok {
		delete(m.runs, runID)
	}
	m.mu.Unlock()
	if ok {
		state.control.Finish()
	}
}

func (m *InMemoryRunManager) lookupControl(runID string) (*RunControl, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.runs[runID]
	if !ok {
		return nil, false
	}
	return state.control, true
}

func IsRunInterrupted(err error) bool {
	return errors.Is(err, ErrRunInterrupted)
}
