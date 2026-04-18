package contracts

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/config"
	"agent-platform-runner-go/internal/stream"
)

const (
	defaultRunReaperInterval        = 30 * time.Second
	defaultRunMaxBackgroundDuration = 10 * time.Minute
	defaultRunCompletedRetention    = 10 * time.Minute
	defaultRunEventBusMaxEvents     = 10000
	defaultRunMaxDisconnectedWait   = 10 * time.Minute
	defaultRunMaxObserversPerRun    = 16
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

type ActiveRunConflictError struct {
	ChatID string
	RunIDs []string
}

func (e *ActiveRunConflictError) Error() string {
	if e == nil {
		return ""
	}
	return "multiple active runs found for chat"
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
	observerCnt atomic.Int32

	mu                    sync.Mutex
	steerQueue            []api.SteerRequest
	submitWaiters         map[string]*submitWaiter
	pendingSubmits        map[string]SubmitResult
	resolvedSubmits       map[string]SubmitResult
	expectedSubmitAwaitID string
	state                 RunLoopState

	maxDisconnectedWait time.Duration
	observerChanged     chan struct{}
}

func NewRunControl(parent context.Context, runID string) *RunControl {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	control := &RunControl{
		runID:               runID,
		ctx:                 ctx,
		cancel:              cancel,
		submitWaiters:       map[string]*submitWaiter{},
		pendingSubmits:      map[string]SubmitResult{},
		resolvedSubmits:     map[string]SubmitResult{},
		state:               RunLoopStateIdle,
		maxDisconnectedWait: defaultRunMaxDisconnectedWait,
		observerChanged:     make(chan struct{}, 1),
	}
	control.observerCnt.Store(1)
	return control
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
	state := c.State()
	if state != RunLoopStateCancelled && state != RunLoopStateFailed {
		c.TransitionState(RunLoopStateCompleted)
	}
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

func (c *RunControl) AwaitSubmit(ctx context.Context, awaitingID string) (SubmitResult, error) {
	return c.AwaitSubmitWithTimeout(ctx, awaitingID, 0)
}

func (c *RunControl) AwaitSubmitWithTimeout(ctx context.Context, awaitingID string, timeout time.Duration) (SubmitResult, error) {
	if c == nil {
		return SubmitResult{}, ErrRunControlUnavailable
	}
	if awaitingID == "" {
		return SubmitResult{}, ErrFrontendSubmitMissingAwaitID
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.interrupted.Load() {
		return SubmitResult{}, ErrRunInterrupted
	}
	if c.finished.Load() {
		return SubmitResult{}, ErrRunFinished
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
	if _, exists := c.submitWaiters[awaitingID]; exists {
		c.mu.Unlock()
		return SubmitResult{}, ErrFrontendSubmitAlreadyWaiting
	}
	if pending, exists := c.pendingSubmits[awaitingID]; exists {
		delete(c.pendingSubmits, awaitingID)
		if c.expectedSubmitAwaitID == awaitingID {
			c.expectedSubmitAwaitID = ""
		}
		c.mu.Unlock()
		return pending, nil
	}
	c.submitWaiters[awaitingID] = waiter
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if current, exists := c.submitWaiters[awaitingID]; exists && current == waiter {
			delete(c.submitWaiters, awaitingID)
		}
		c.mu.Unlock()
	}()

	connectedRemaining := timeout
	disconnectedRemaining := c.maxDisconnectedWaitValue()
	lastHasObserver := c.HasObserver()
	lastTick := time.Now()
	timer := newWaitTimer(lastHasObserver, connectedRemaining, disconnectedRemaining)
	defer stopWaitTimer(timer)

	for {
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
		case <-c.observerChanged:
		case <-waitTimerChan(timer):
		}

		now := time.Now()
		connectedRemaining, disconnectedRemaining, expired := consumeWaitBudget(now.Sub(lastTick), lastHasObserver, connectedRemaining, disconnectedRemaining)
		if expired {
			return SubmitResult{}, context.DeadlineExceeded
		}
		lastTick = now
		lastHasObserver = c.HasObserver()
		resetWaitTimer(timer, lastHasObserver, connectedRemaining, disconnectedRemaining)
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
	if resolved, exists := c.resolvedSubmits[req.AwaitingID]; exists {
		c.mu.Unlock()
		detail := resolved.Detail
		if detail == "" {
			detail = "Frontend submit already resolved"
		}
		return SubmitAck{Accepted: false, Status: "already_resolved", Detail: detail}
	}
	waiter, ok := c.submitWaiters[req.AwaitingID]
	if ok {
		delete(c.submitWaiters, req.AwaitingID)
		if c.expectedSubmitAwaitID == req.AwaitingID {
			c.expectedSubmitAwaitID = ""
		}
		c.resolvedSubmits[req.AwaitingID] = SubmitResult{
			Request: req,
			Status:  "accepted",
			Detail:  "Frontend submit accepted",
		}
	}
	if !ok && req.AwaitingID != "" && req.AwaitingID == c.expectedSubmitAwaitID && !c.interrupted.Load() && !c.finished.Load() {
		accepted := SubmitResult{
			Request: req,
			Status:  "accepted",
			Detail:  "Frontend submit accepted",
		}
		c.pendingSubmits[req.AwaitingID] = accepted
		c.resolvedSubmits[req.AwaitingID] = accepted
		c.expectedSubmitAwaitID = ""
		c.mu.Unlock()
		return SubmitAck{Accepted: true, Status: "accepted", Detail: "Frontend submit accepted"}
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

func (c *RunControl) ExpectSubmit(awaitingID string) {
	if c == nil || awaitingID == "" {
		return
	}
	c.mu.Lock()
	c.expectedSubmitAwaitID = awaitingID
	c.mu.Unlock()
}

func (c *RunControl) ClearExpectedSubmit(awaitingID string) {
	if c == nil || awaitingID == "" {
		return
	}
	c.mu.Lock()
	if c.expectedSubmitAwaitID == awaitingID {
		c.expectedSubmitAwaitID = ""
	}
	c.mu.Unlock()
}

func (c *RunControl) SetObserverCount(count int32) {
	if c == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	c.observerCnt.Store(count)
	select {
	case c.observerChanged <- struct{}{}:
	default:
	}
}

func (c *RunControl) HasObserver() bool {
	return c != nil && c.observerCnt.Load() > 0
}

func (c *RunControl) ObserverCount() int32 {
	if c == nil {
		return 0
	}
	return c.observerCnt.Load()
}

func (c *RunControl) SetMaxDisconnectedWait(wait time.Duration) {
	if c == nil || wait <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxDisconnectedWait = wait
}

func (c *RunControl) maxDisconnectedWaitValue() time.Duration {
	if c == nil {
		return defaultRunMaxDisconnectedWait
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxDisconnectedWait
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

func newWaitTimer(hasObserver bool, connectedRemaining time.Duration, disconnectedRemaining time.Duration) *time.Timer {
	if delay, ok := nextWaitDelay(hasObserver, connectedRemaining, disconnectedRemaining); ok {
		return time.NewTimer(delay)
	}
	return nil
}

func waitTimerChan(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}

func stopWaitTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func resetWaitTimer(timer *time.Timer, hasObserver bool, connectedRemaining time.Duration, disconnectedRemaining time.Duration) {
	if timer == nil {
		return
	}
	delay, ok := nextWaitDelay(hasObserver, connectedRemaining, disconnectedRemaining)
	if !ok {
		stopWaitTimer(timer)
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}

func nextWaitDelay(hasObserver bool, connectedRemaining time.Duration, disconnectedRemaining time.Duration) (time.Duration, bool) {
	if hasObserver {
		if connectedRemaining > 0 {
			return connectedRemaining, true
		}
		return 0, false
	}
	if disconnectedRemaining > 0 {
		return disconnectedRemaining, true
	}
	return 0, false
}

func consumeWaitBudget(elapsed time.Duration, hadObserver bool, connectedRemaining time.Duration, disconnectedRemaining time.Duration) (time.Duration, time.Duration, bool) {
	if elapsed <= 0 {
		return connectedRemaining, disconnectedRemaining, false
	}
	if hadObserver && connectedRemaining > 0 {
		connectedRemaining -= elapsed
		if connectedRemaining <= 0 {
			return 0, disconnectedRemaining, true
		}
	}
	if !hadObserver && disconnectedRemaining > 0 {
		disconnectedRemaining -= elapsed
		if disconnectedRemaining <= 0 {
			return connectedRemaining, 0, true
		}
	}
	return connectedRemaining, disconnectedRemaining, false
}

type managedRun struct {
	run         ActiveRun
	control     *RunControl
	eventBus    *stream.RunEventBus
	startedAt   time.Time
	completedAt time.Time
}

type InMemoryRunManager struct {
	mu                    sync.Mutex
	runs                  map[string]*managedRun
	reaperStop            chan struct{}
	reaperOnce            sync.Once
	reaperInterval        time.Duration
	maxBackgroundDuration time.Duration
	completedRetention    time.Duration
	eventBusMaxEvents     int
	maxDisconnectedWait   time.Duration
	maxObserversPerRun    int
}

func NewInMemoryRunManager() *InMemoryRunManager {
	return &InMemoryRunManager{
		runs:                  map[string]*managedRun{},
		reaperStop:            make(chan struct{}),
		reaperInterval:        defaultRunReaperInterval,
		maxBackgroundDuration: defaultRunMaxBackgroundDuration,
		completedRetention:    defaultRunCompletedRetention,
		eventBusMaxEvents:     defaultRunEventBusMaxEvents,
		maxDisconnectedWait:   defaultRunMaxDisconnectedWait,
		maxObserversPerRun:    defaultRunMaxObserversPerRun,
	}
}

func (m *InMemoryRunManager) ConfigureRunLifecycle(cfg config.RunConfig) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.ReaperIntervalMs > 0 {
		m.reaperInterval = time.Duration(cfg.ReaperIntervalMs) * time.Millisecond
	}
	if cfg.MaxBackgroundDurationMs > 0 {
		m.maxBackgroundDuration = time.Duration(cfg.MaxBackgroundDurationMs) * time.Millisecond
	}
	if cfg.CompletedRetentionMs > 0 {
		m.completedRetention = time.Duration(cfg.CompletedRetentionMs) * time.Millisecond
	}
	if cfg.EventBusMaxEvents > 0 {
		m.eventBusMaxEvents = cfg.EventBusMaxEvents
	}
	if cfg.MaxDisconnectedWaitMs > 0 {
		m.maxDisconnectedWait = time.Duration(cfg.MaxDisconnectedWaitMs) * time.Millisecond
	}
	if cfg.MaxObserversPerRun > 0 {
		m.maxObserversPerRun = cfg.MaxObserversPerRun
	}
	m.startReaper()
}

func (m *InMemoryRunManager) Register(_ context.Context, session QuerySession) (context.Context, *RunControl, ActiveRun) {
	m.startReaper()
	m.mu.Lock()
	defer m.mu.Unlock()

	control := NewRunControl(context.Background(), session.RunID)
	control.SetMaxDisconnectedWait(m.maxDisconnectedWait)
	run := ActiveRun{RunID: session.RunID, ChatID: session.ChatID, AgentKey: session.AgentKey}
	eventBus := stream.NewRunEventBus(m.eventBusMaxEvents, m.maxObserversPerRun, func(count int) {
		control.SetObserverCount(int32(count))
	})
	control.SetObserverCount(0)
	m.runs[session.RunID] = &managedRun{
		run:       run,
		control:   control,
		eventBus:  eventBus,
		startedAt: time.Now(),
	}
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
	m.mu.Unlock()
	if !ok {
		return InterruptAck{Accepted: false, Status: "unmatched", Detail: "No active run found"}
	}
	if !state.control.Interrupt() {
		return InterruptAck{Accepted: false, Status: "unmatched", Detail: "Run is no longer active"}
	}
	return InterruptAck{Accepted: true, Status: "accepted", Detail: "Interrupt accepted"}
}

func (m *InMemoryRunManager) Finish(runID string) {
	m.mu.Lock()
	state, ok := m.runs[runID]
	if ok {
		state.completedAt = time.Now()
	}
	m.mu.Unlock()
	if ok {
		state.control.Finish()
	}
}

func (m *InMemoryRunManager) AttachObserver(runID string, afterSeq int64) (*stream.Observer, error) {
	state, ok := m.lookupRun(runID)
	if !ok {
		return nil, ErrRunControlUnavailable
	}
	return state.eventBus.Subscribe(afterSeq)
}

func (m *InMemoryRunManager) DetachObserver(runID string, observerID string) {
	state, ok := m.lookupRun(runID)
	if !ok || state.eventBus == nil {
		return
	}
	state.eventBus.Unsubscribe(observerID)
}

func (m *InMemoryRunManager) RunStatus(runID string) (RunStatusInfo, bool) {
	state, ok := m.lookupRun(runID)
	if !ok {
		return RunStatusInfo{}, false
	}
	info := RunStatusInfo{
		RunID:         state.run.RunID,
		ChatID:        state.run.ChatID,
		AgentKey:      state.run.AgentKey,
		State:         state.control.State(),
		LastSeq:       state.eventBus.LatestSeq(),
		OldestSeq:     state.eventBus.OldestSeq(),
		ObserverCount: state.eventBus.ObserverCount(),
		StartedAt:     state.startedAt.UnixMilli(),
	}
	if !state.completedAt.IsZero() {
		info.CompletedAt = state.completedAt.UnixMilli()
	}
	return info, true
}

func (m *InMemoryRunManager) ActiveRunForChat(chatID string) (RunStatusInfo, bool, error) {
	if strings.TrimSpace(chatID) == "" {
		return RunStatusInfo{}, false, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var (
		match   *managedRun
		runIDs  []string
		chatKey = strings.TrimSpace(chatID)
	)
	for _, state := range m.runs {
		if state == nil || state.eventBus == nil || !state.completedAt.IsZero() {
			continue
		}
		if strings.TrimSpace(state.run.ChatID) != chatKey {
			continue
		}
		runIDs = append(runIDs, state.run.RunID)
		if match == nil {
			match = state
			continue
		}
		if state.startedAt.After(match.startedAt) {
			match = state
		}
	}

	if len(runIDs) == 0 || match == nil {
		return RunStatusInfo{}, false, nil
	}
	if len(runIDs) > 1 {
		return RunStatusInfo{}, false, &ActiveRunConflictError{
			ChatID: chatKey,
			RunIDs: append([]string(nil), runIDs...),
		}
	}

	info := RunStatusInfo{
		RunID:         match.run.RunID,
		ChatID:        match.run.ChatID,
		AgentKey:      match.run.AgentKey,
		State:         match.control.State(),
		LastSeq:       match.eventBus.LatestSeq(),
		OldestSeq:     match.eventBus.OldestSeq(),
		ObserverCount: match.eventBus.ObserverCount(),
		StartedAt:     match.startedAt.UnixMilli(),
	}
	if !match.completedAt.IsZero() {
		info.CompletedAt = match.completedAt.UnixMilli()
	}
	return info, true, nil
}

func (m *InMemoryRunManager) EventBus(runID string) (*stream.RunEventBus, bool) {
	state, ok := m.lookupRun(runID)
	if !ok || state.eventBus == nil {
		return nil, false
	}
	return state.eventBus, true
}

func (m *InMemoryRunManager) startReaper() {
	if m == nil {
		return
	}
	m.reaperOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(m.reaperInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					m.reapExpiredRuns()
				case <-m.reaperStop:
					return
				}
			}
		}()
	})
}

func (m *InMemoryRunManager) reapExpiredRuns() {
	if m == nil {
		return
	}
	now := time.Now()
	var toInterrupt []*managedRun
	var toDelete []string

	m.mu.Lock()
	for runID, state := range m.runs {
		if state == nil {
			continue
		}
		if !state.completedAt.IsZero() {
			if now.Sub(state.completedAt) > m.completedRetention {
				toDelete = append(toDelete, runID)
			}
			continue
		}
		if state.eventBus.ObserverCount() > 0 {
			continue
		}
		if now.Sub(state.startedAt) > m.maxBackgroundDuration {
			toInterrupt = append(toInterrupt, state)
		}
	}
	for _, runID := range toDelete {
		delete(m.runs, runID)
	}
	m.mu.Unlock()

	for _, state := range toInterrupt {
		if state != nil && state.eventBus != nil {
			state.eventBus.Publish(stream.EventData{
				Seq:       state.eventBus.LatestSeq() + 1,
				Type:      "run.expired",
				Timestamp: time.Now().UnixMilli(),
				Payload:   map[string]any{"runId": state.run.RunID},
			})
		}
		if !state.control.Interrupt() {
			log.Printf("[runctl] reaper skip interrupt run=%s state=%s", state.run.RunID, state.control.State())
		}
	}
}

func (m *InMemoryRunManager) lookupControl(runID string) (*RunControl, bool) {
	state, ok := m.lookupRun(runID)
	if !ok {
		return nil, false
	}
	return state.control, true
}

func (m *InMemoryRunManager) lookupRun(runID string) (*managedRun, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.runs[runID]
	return state, ok
}

func IsRunInterrupted(err error) bool {
	return errors.Is(err, ErrRunInterrupted)
}
