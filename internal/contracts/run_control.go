package contracts

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform/internal/api"
)

const (
	InterruptSourceHTTPAPI     = "http_api"
	InterruptSourceWSAPI       = "ws_api"
	InterruptSourceProxyWS     = "proxy_ws"
	InterruptSourceServerSetup = "server_setup"
	InterruptSourceReaper      = "reaper"
	InterruptSourceUnknown     = "unknown"
)

const (
	InterruptReasonUserCancelled        = "user_cancelled"
	InterruptReasonProxyInterrupt       = "proxy_interrupt"
	InterruptReasonRunExpired           = "run_expired"
	InterruptReasonEventBusUnavailable  = "event_bus_unavailable"
	InterruptReasonObserverAttachFailed = "observer_attach_failed"
	InterruptReasonStreamWriterFailed   = "stream_writer_failed"
	InterruptReasonRunInterrupted       = "run_interrupted"
)

type runControlContextKey struct{}

type SubmitResult struct {
	Request api.SubmitRequest
	Status  string
	Detail  string
}

type InterruptInfo struct {
	Source        string
	Reason        string
	Detail        string
	RequestID     string
	ChatID        string
	InterruptedAt time.Time
}

func InterruptInfoFromRequest(req api.InterruptRequest) InterruptInfo {
	detail := strings.TrimSpace(req.InterruptDetail)
	if detail == "" {
		detail = strings.TrimSpace(req.Message)
	}
	return InterruptInfo{
		Source:    req.InterruptSource,
		Reason:    req.InterruptReason,
		Detail:    detail,
		RequestID: req.RequestID,
		ChatID:    req.ChatID,
	}
}

func NormalizeInterruptInfo(info InterruptInfo) InterruptInfo {
	info.Source = strings.TrimSpace(info.Source)
	info.Reason = strings.TrimSpace(info.Reason)
	info.Detail = strings.TrimSpace(info.Detail)
	info.RequestID = strings.TrimSpace(info.RequestID)
	info.ChatID = strings.TrimSpace(info.ChatID)
	if info.Source == "" {
		info.Source = InterruptSourceUnknown
	}
	if info.Reason == "" {
		info.Reason = InterruptReasonRunInterrupted
	}
	if info.Detail == "" {
		info.Detail = "Run interrupted"
	}
	if info.InterruptedAt.IsZero() {
		info.InterruptedAt = time.Now()
	}
	return info
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

	mu              sync.Mutex
	steerQueue      []api.SteerRequest
	submitWaiters   map[string]*submitWaiter
	pendingSubmits  map[string]SubmitResult
	resolvedSubmits map[string]SubmitResult
	awaitingSubmits map[string]AwaitingSubmitContext
	interruptInfo   InterruptInfo
	state           RunLoopState
	accessLevel     string
	accessVersion   int64

	accessLevelChanged chan struct{}
}

func NewRunControl(parent context.Context, runID string) *RunControl {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	control := &RunControl{
		runID:              runID,
		ctx:                ctx,
		cancel:             cancel,
		submitWaiters:      map[string]*submitWaiter{},
		pendingSubmits:     map[string]SubmitResult{},
		resolvedSubmits:    map[string]SubmitResult{},
		awaitingSubmits:    map[string]AwaitingSubmitContext{},
		state:              RunLoopStateIdle,
		accessLevel:        AccessLevelDefault,
		accessVersion:      1,
		accessLevelChanged: make(chan struct{}, 1),
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

func (c *RunControl) InterruptInfo() (InterruptInfo, bool) {
	if c == nil || !c.interrupted.Load() {
		return InterruptInfo{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(c.interruptInfo.Source) == "" && strings.TrimSpace(c.interruptInfo.Reason) == "" {
		return NormalizeInterruptInfo(InterruptInfo{}), true
	}
	return c.interruptInfo, true
}

func (c *RunControl) Finished() bool {
	return c != nil && c.finished.Load()
}

func (c *RunControl) Interrupt(info InterruptInfo) bool {
	if c == nil {
		return false
	}
	if !c.interrupted.CompareAndSwap(false, true) {
		return false
	}
	info = NormalizeInterruptInfo(info)
	c.mu.Lock()
	c.interruptInfo = info
	c.mu.Unlock()
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
	result, _, err := c.awaitSubmitWithTimeout(ctx, awaitingID, timeout, -1)
	return result, err
}

func (c *RunControl) AwaitSubmitWithTimeoutOrAccessLevelChange(ctx context.Context, awaitingID string, timeout time.Duration, afterVersion int64) (SubmitResult, bool, error) {
	if _, currentVersion := c.AccessLevelSnapshot(); currentVersion != afterVersion {
		return SubmitResult{}, true, nil
	}
	return c.awaitSubmitWithTimeout(ctx, awaitingID, timeout, afterVersion)
}

func (c *RunControl) awaitSubmitWithTimeout(ctx context.Context, awaitingID string, timeout time.Duration, breakOnAccessVersion int64) (SubmitResult, bool, error) {
	if c == nil {
		return SubmitResult{}, false, ErrRunControlUnavailable
	}
	if awaitingID == "" {
		return SubmitResult{}, false, ErrFrontendSubmitMissingAwaitID
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.interrupted.Load() {
		return SubmitResult{}, false, ErrRunInterrupted
	}
	if c.finished.Load() {
		return SubmitResult{}, false, ErrRunFinished
	}

	waiter := &submitWaiter{ch: make(chan SubmitResult, 1)}
	c.mu.Lock()
	if c.interrupted.Load() {
		c.mu.Unlock()
		return SubmitResult{}, false, ErrRunInterrupted
	}
	if c.finished.Load() {
		c.mu.Unlock()
		return SubmitResult{}, false, ErrRunFinished
	}
	if _, exists := c.submitWaiters[awaitingID]; exists {
		c.mu.Unlock()
		return SubmitResult{}, false, ErrFrontendSubmitAlreadyWaiting
	}
	awaitingCtx := c.awaitingSubmits[awaitingID]
	if pending, exists := c.pendingSubmits[awaitingID]; exists {
		delete(c.pendingSubmits, awaitingID)
		c.mu.Unlock()
		return pending, false, nil
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

	var timer *time.Timer
	if timeout > 0 && !awaitingCtx.NoTimeout {
		timer = time.NewTimer(timeout)
		defer stopWaitTimer(timer)
	}
	var accessChanged <-chan struct{}
	if breakOnAccessVersion >= 0 {
		accessChanged = c.accessLevelChanged
	}

	for {
		select {
		case result := <-waiter.ch:
			switch result.Status {
			case "interrupted":
				return SubmitResult{}, false, ErrRunInterrupted
			case "finished":
				return SubmitResult{}, false, ErrRunFinished
			default:
				return result, false, nil
			}
		case <-ctx.Done():
			return SubmitResult{}, false, ctx.Err()
		case <-c.ctx.Done():
			if c.interrupted.Load() {
				return SubmitResult{}, false, ErrRunInterrupted
			}
			if c.finished.Load() {
				return SubmitResult{}, false, ErrRunFinished
			}
			return SubmitResult{}, false, context.Canceled
		case <-accessChanged:
			if breakOnAccessVersion >= 0 {
				if _, currentVersion := c.AccessLevelSnapshot(); currentVersion != breakOnAccessVersion {
					return SubmitResult{}, true, nil
				}
			}
		case <-waitTimerChan(timer):
			c.clearTimedOutSubmit(awaitingID, waiter)
			return SubmitResult{}, false, context.DeadlineExceeded
		}
		if breakOnAccessVersion >= 0 {
			if _, currentVersion := c.AccessLevelSnapshot(); currentVersion != breakOnAccessVersion {
				return SubmitResult{}, true, nil
			}
		}
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

func (c *RunControl) SetInitialAccessLevel(accessLevel string) {
	if c == nil {
		return
	}
	normalized, ok := NormalizeAccessLevel(accessLevel)
	if !ok {
		normalized = AccessLevelDefault
	}
	c.mu.Lock()
	c.accessLevel = normalized
	if c.accessVersion <= 0 {
		c.accessVersion = 1
	}
	c.mu.Unlock()
}

func (c *RunControl) UpdateAccessLevel(accessLevel string) (string, string, int64, bool) {
	if c == nil {
		return "", "", 0, false
	}
	normalized, ok := NormalizeAccessLevel(accessLevel)
	if !ok {
		normalized = AccessLevelDefault
	}
	c.mu.Lock()
	previous := c.accessLevel
	if previous == "" {
		previous = AccessLevelDefault
	}
	version := c.accessVersion
	if version <= 0 {
		version = 1
	}
	if previous == normalized {
		c.accessLevel = normalized
		c.accessVersion = version
		c.mu.Unlock()
		return previous, normalized, version, false
	}
	version++
	c.accessLevel = normalized
	c.accessVersion = version
	c.mu.Unlock()
	select {
	case c.accessLevelChanged <- struct{}{}:
	default:
	}
	return previous, normalized, version, true
}

func (c *RunControl) AccessLevelSnapshot() (string, int64) {
	if c == nil {
		return AccessLevelDefault, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	accessLevel := c.accessLevel
	if accessLevel == "" {
		accessLevel = AccessLevelDefault
	}
	version := c.accessVersion
	if version <= 0 {
		version = 1
	}
	return accessLevel, version
}

func (c *RunControl) ResolveSubmit(req api.SubmitRequest) SubmitAck {
	if c == nil {
		return SubmitAck{Accepted: false, Status: "unmatched", SubmitID: req.SubmitID, Detail: "No active run found"}
	}
	c.mu.Lock()
	if resolved, exists := c.resolvedSubmits[req.AwaitingID]; exists {
		c.mu.Unlock()
		if strings.TrimSpace(req.SubmitID) != "" && strings.TrimSpace(resolved.Request.SubmitID) == strings.TrimSpace(req.SubmitID) {
			detail := resolved.Detail
			if detail == "" {
				detail = "Frontend submit accepted"
			}
			return SubmitAck{Accepted: true, Status: "accepted", SubmitID: req.SubmitID, Detail: detail}
		}
		detail := resolved.Detail
		if detail == "" {
			detail = "Frontend submit already resolved"
		}
		return SubmitAck{Accepted: false, Status: "already_resolved", SubmitID: firstNonBlankSubmitID(resolved.Request.SubmitID, req.SubmitID), Detail: detail}
	}
	waiter, ok := c.submitWaiters[req.AwaitingID]
	if ok {
		delete(c.submitWaiters, req.AwaitingID)
		delete(c.awaitingSubmits, req.AwaitingID)
		c.resolvedSubmits[req.AwaitingID] = SubmitResult{
			Request: req,
			Status:  "accepted",
			Detail:  "Frontend submit accepted",
		}
	}
	if _, exists := c.awaitingSubmits[req.AwaitingID]; !ok && req.AwaitingID != "" && exists && !c.interrupted.Load() && !c.finished.Load() {
		accepted := SubmitResult{
			Request: req,
			Status:  "accepted",
			Detail:  "Frontend submit accepted",
		}
		c.pendingSubmits[req.AwaitingID] = accepted
		c.resolvedSubmits[req.AwaitingID] = accepted
		delete(c.awaitingSubmits, req.AwaitingID)
		c.mu.Unlock()
		return SubmitAck{Accepted: true, Status: "accepted", SubmitID: req.SubmitID, Detail: "Frontend submit accepted"}
	}
	c.mu.Unlock()
	if !ok {
		return SubmitAck{Accepted: false, Status: "unmatched", SubmitID: req.SubmitID, Detail: "No pending frontend submit waiter found"}
	}
	if !waiter.deliver(SubmitResult{
		Request: req,
		Status:  "accepted",
		Detail:  "Frontend submit accepted",
	}) {
		return SubmitAck{Accepted: false, Status: "unmatched", SubmitID: req.SubmitID, Detail: "Frontend submit waiter is no longer active"}
	}
	return SubmitAck{Accepted: true, Status: "accepted", SubmitID: req.SubmitID, Detail: "Frontend submit accepted"}
}

func (c *RunControl) LookupAwaiting(awaitingID string) (AwaitingSubmitContext, bool) {
	if c == nil || awaitingID == "" {
		return AwaitingSubmitContext{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, ok := c.awaitingSubmits[awaitingID]
	if !ok {
		return AwaitingSubmitContext{}, false
	}
	return ctx.Clone(), true
}

func (c *RunControl) HasNoTimeoutAwaiting() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, awaiting := range c.awaitingSubmits {
		if awaiting.NoTimeout {
			return true
		}
	}
	return false
}

func (c *RunControl) LookupResolvedSubmit(awaitingID string) (SubmitAck, bool) {
	if c == nil || awaitingID == "" {
		return SubmitAck{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	resolved, ok := c.resolvedSubmits[awaitingID]
	if !ok {
		return SubmitAck{}, false
	}
	detail := strings.TrimSpace(resolved.Detail)
	if detail == "" {
		detail = "Frontend submit already resolved"
	}
	return SubmitAck{
		Accepted: false,
		Status:   "already_resolved",
		SubmitID: resolved.Request.SubmitID,
		Detail:   detail,
	}, true
}

func firstNonBlankSubmitID(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (c *RunControl) ExpectSubmit(ctx AwaitingSubmitContext) {
	if c == nil || ctx.AwaitingID == "" {
		return
	}
	c.mu.Lock()
	c.awaitingSubmits[ctx.AwaitingID] = ctx.Clone()
	c.mu.Unlock()
}

func (c *RunControl) ClearExpectedSubmit(awaitingID string) {
	if c == nil || awaitingID == "" {
		return
	}
	c.mu.Lock()
	delete(c.awaitingSubmits, awaitingID)
	c.mu.Unlock()
}

func (c *RunControl) clearTimedOutSubmit(awaitingID string, waiter *submitWaiter) {
	if c == nil || awaitingID == "" {
		return
	}
	c.mu.Lock()
	if current, exists := c.submitWaiters[awaitingID]; exists && current == waiter {
		delete(c.submitWaiters, awaitingID)
	}
	delete(c.awaitingSubmits, awaitingID)
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

func (c *RunControl) closeWaiters(status string, detail string) {
	c.mu.Lock()
	waiters := c.submitWaiters
	c.submitWaiters = map[string]*submitWaiter{}
	c.awaitingSubmits = map[string]AwaitingSubmitContext{}
	c.mu.Unlock()
	for _, waiter := range waiters {
		waiter.deliver(SubmitResult{Status: status, Detail: detail})
	}
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
