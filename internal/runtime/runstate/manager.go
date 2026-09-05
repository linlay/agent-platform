package runstate

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
	"agent-platform/internal/runenv"
	"agent-platform/internal/stream"
)

const (
	defaultRunReaperInterval        = 30 * time.Second
	defaultRunMaxBackgroundDuration = 0
	defaultRunCompletedRetention    = 10 * time.Second
	defaultRunEventBusMaxEvents     = 10000
	defaultRunMaxObserversPerRun    = 8
)

type managedRun struct {
	run                 contracts.ActiveRun
	control             *contracts.RunControl
	eventBus            *stream.RunEventBus
	webClientTarget     contracts.WebClientTarget
	runOrigin           *contracts.RunOrigin
	startedAt           time.Time
	activeSince         time.Time
	reaperStartOverride bool
	completedAt         time.Time
	recoveredAwaitingID string
	recoveredClaimed    bool
	runEnvironment      *runenv.Scope
}

type Manager struct {
	mu                    sync.Mutex
	runs                  map[string]*managedRun
	reaperStop            chan struct{}
	reaperOnce            sync.Once
	reaperInterval        time.Duration
	maxBackgroundDuration time.Duration
	completedRetention    time.Duration
	eventBusMaxEvents     int
	maxObserversPerRun    int
	chatMaintenance       map[string]contracts.CompactControlHandle
	chatMaintenanceResult map[string]contracts.CompactControlHandle
	chatQueryAdmissions   map[string]map[string]int
}

func NewManager() *Manager {
	return &Manager{
		runs:                  map[string]*managedRun{},
		reaperStop:            make(chan struct{}),
		reaperInterval:        defaultRunReaperInterval,
		maxBackgroundDuration: defaultRunMaxBackgroundDuration,
		completedRetention:    defaultRunCompletedRetention,
		eventBusMaxEvents:     defaultRunEventBusMaxEvents,
		maxObserversPerRun:    defaultRunMaxObserversPerRun,
		chatMaintenance:       map[string]contracts.CompactControlHandle{},
		chatMaintenanceResult: map[string]contracts.CompactControlHandle{},
		chatQueryAdmissions:   map[string]map[string]int{},
	}
}

func (m *Manager) Register(_ context.Context, session contracts.QuerySession) (context.Context, *contracts.RunControl, contracts.ActiveRun) {
	m.startReaper()
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.registerLocked(session)
}

func (m *Manager) RegisterExclusiveForChat(_ context.Context, session contracts.QuerySession) (contracts.ExclusiveRunRegistration, error) {
	m.startReaper()
	m.mu.Lock()
	defer m.mu.Unlock()

	scopeID := querySessionRunScopeID(session)
	m.releaseChatQueryLocked(scopeID, session.RequestID)
	if maintenance := m.chatMaintenance[scopeID]; maintenance.Valid() {
		return contracts.ExclusiveRunRegistration{}, &contracts.ChatMaintenanceConflictError{ChatID: scopeID, Detail: "compact_in_progress"}
	}
	if scopeID != "" {
		match, runIDs := m.activeRunMatchLocked(scopeID)
		if len(runIDs) > 1 {
			return contracts.ExclusiveRunRegistration{}, &contracts.ActiveRunConflictError{
				ChatID: strings.TrimSpace(session.ChatID),
				RunIDs: append([]string(nil), runIDs...),
			}
		}
		if len(runIDs) == 1 && match != nil {
			return contracts.ExclusiveRunRegistration{
				ActiveRun: runStatusInfoFromManagedRun(match),
			}, nil
		}
	}

	runCtx, control, run := m.registerLocked(session)
	return contracts.ExclusiveRunRegistration{
		Context:    runCtx,
		Control:    control,
		Run:        run,
		Registered: true,
	}, nil
}

func (m *Manager) ReserveChatQuery(chatID string, requestID string) (func(), error) {
	chatID = strings.TrimSpace(chatID)
	requestID = strings.TrimSpace(requestID)
	if chatID == "" || requestID == "" {
		return nil, fmt.Errorf("chatId and requestId are required")
	}
	m.mu.Lock()
	if m.chatMaintenance[chatID].Valid() {
		m.mu.Unlock()
		return nil, &contracts.ChatMaintenanceConflictError{ChatID: chatID, Detail: "compact_in_progress"}
	}
	if m.chatQueryAdmissions[chatID] == nil {
		m.chatQueryAdmissions[chatID] = map[string]int{}
	}
	m.chatQueryAdmissions[chatID][requestID]++
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			m.releaseChatQueryLocked(chatID, requestID)
			m.mu.Unlock()
		})
	}, nil
}

func (m *Manager) releaseChatQueryLocked(chatID string, requestID string) {
	requests := m.chatQueryAdmissions[strings.TrimSpace(chatID)]
	requestID = strings.TrimSpace(requestID)
	if requests == nil || requestID == "" {
		return
	}
	if requests[requestID] <= 1 {
		delete(requests, requestID)
	} else {
		requests[requestID]--
	}
	if len(requests) == 0 {
		delete(m.chatQueryAdmissions, strings.TrimSpace(chatID))
	}
}

func (m *Manager) registerLocked(session contracts.QuerySession) (context.Context, *contracts.RunControl, contracts.ActiveRun) {
	control := contracts.NewRunControl(context.Background(), session.RunID)
	control.SetInitialAccessLevel(session.AccessLevel)
	if session.SupportsContextCompaction {
		control.EnableContextCompact()
	}
	owner := contracts.ResolveRunOwner(session.RunOwner)
	run := contracts.ActiveRun{
		RunID:             session.RunID,
		ChatID:            session.ChatID,
		AgentKey:          owner.AgentKey,
		TeamID:            owner.TeamID,
		ExecutionAgentKey: owner.ExecutionAgentKey,
		ScopeID:           strings.TrimSpace(session.RunScopeID),
		EditingMode:       session.EditingMode,
	}
	startedAt := time.Now()
	if session.StartedAtMillis != 0 {
		// The server validates a persisted override before registration. Do not
		// substitute a new wall clock here: that would sever the run manager from
		// the immutable lifecycle record after a process restart.
		startedAt = time.UnixMilli(session.StartedAtMillis)
	}
	eventBus := stream.NewRunEventBus(m.eventBusMaxEvents, m.maxObserversPerRun, func(count int) {
		control.SetObserverCount(int32(count))
	})
	control.SetObserverCount(0)
	m.runs[session.RunID] = &managedRun{
		run:             run,
		control:         control,
		eventBus:        eventBus,
		webClientTarget: session.WebClientTarget,
		runOrigin:       cloneRunOrigin(session.RunOrigin),
		runEnvironment:  session.RunEnvironment,
		startedAt:       startedAt,
		activeSince:     startedAt,
	}
	return contracts.WithRunControl(control.Context(), control), control, run
}

// BindWebClientTarget atomically makes target the action destination for the
// run. A successful attach is intentionally last-writer-wins, while callers
// without a usable WebClient target cannot clear an existing binding.
func (m *Manager) BindWebClientTarget(runID string, target contracts.WebClientTarget) bool {
	runID = strings.TrimSpace(runID)
	if m == nil || runID == "" || target.IsZero() {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.runs[runID]
	if state == nil {
		return false
	}
	state.webClientTarget = target
	return true
}

func (m *Manager) ResolveWebClientTarget(runID string) (contracts.WebClientTarget, bool) {
	runID = strings.TrimSpace(runID)
	if m == nil || runID == "" {
		return contracts.WebClientTarget{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.runs[runID]
	if state == nil || state.webClientTarget.IsZero() {
		return contracts.WebClientTarget{}, false
	}
	return state.webClientTarget, true
}

func (m *Manager) BindClientTarget(runID string, target contracts.ClientTarget) bool {
	return m.BindWebClientTarget(runID, target)
}

func (m *Manager) ResolveClientTarget(runID string) (contracts.ClientTarget, bool) {
	return m.ResolveWebClientTarget(runID)
}

func (m *Manager) RegisterRecoveredAwaiting(_ context.Context, session contracts.QuerySession, awaitingID string, initialSeq int64) (contracts.RecoveredAwaitingRun, error) {
	m.startReaper()
	awaitingID = strings.TrimSpace(awaitingID)
	if strings.TrimSpace(session.RunID) == "" || awaitingID == "" || initialSeq < 0 {
		return contracts.RecoveredAwaitingRun{}, fmt.Errorf("recovered awaiting identity and cursor are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.runs[session.RunID]; exists {
		return contracts.RecoveredAwaitingRun{}, fmt.Errorf("run already registered: %s", session.RunID)
	}
	if scopeID := querySessionRunScopeID(session); scopeID != "" {
		_, runIDs := m.activeRunMatchLocked(scopeID)
		if len(runIDs) > 0 {
			return contracts.RecoveredAwaitingRun{}, &contracts.ActiveRunConflictError{ChatID: session.ChatID, RunIDs: runIDs}
		}
	}
	runCtx, control, _ := m.registerLocked(session)
	state := m.runs[session.RunID]
	state.activeSince = time.Now()
	state.reaperStartOverride = true
	state.recoveredAwaitingID = awaitingID
	if !state.eventBus.SeedCursor(initialSeq) {
		delete(m.runs, session.RunID)
		control.Finish()
		return contracts.RecoveredAwaitingRun{}, fmt.Errorf("seed recovered run cursor")
	}
	control.TransitionState(contracts.RunLoopStateWaitingSubmit)
	return recoveredAwaitingRunFromManaged(runCtx, state, initialSeq), nil
}

func (m *Manager) ClaimRecoveredAwaiting(runID string, awaitingID string) (contracts.RecoveredAwaitingRun, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.runs[strings.TrimSpace(runID)]
	if state == nil || strings.TrimSpace(state.recoveredAwaitingID) != strings.TrimSpace(awaitingID) || state.recoveredClaimed || !state.completedAt.IsZero() {
		return contracts.RecoveredAwaitingRun{}, false
	}
	state.recoveredClaimed = true
	state.control.TransitionState(contracts.RunLoopStateResuming)
	return recoveredAwaitingRunFromManaged(contracts.WithRunControl(state.control.Context(), state.control), state, state.eventBus.LatestSeq()), true
}

func (m *Manager) ReleaseRecoveredAwaiting(runID string, awaitingID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.runs[strings.TrimSpace(runID)]
	if state == nil || strings.TrimSpace(state.recoveredAwaitingID) != strings.TrimSpace(awaitingID) || !state.recoveredClaimed || !state.completedAt.IsZero() {
		return false
	}
	state.recoveredClaimed = false
	state.control.TransitionState(contracts.RunLoopStateWaitingSubmit)
	return true
}

func (m *Manager) ActivateRecoveredAwaiting(runID string, awaitingID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.runs[strings.TrimSpace(runID)]
	if state == nil || strings.TrimSpace(state.recoveredAwaitingID) != strings.TrimSpace(awaitingID) || !state.recoveredClaimed || !state.completedAt.IsZero() {
		return false
	}
	state.recoveredAwaitingID = ""
	state.recoveredClaimed = false
	state.activeSince = time.Now()
	return true
}

func (m *Manager) IsRecoveredAwaiting(runID string, awaitingID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.runs[strings.TrimSpace(runID)]
	return state != nil && strings.TrimSpace(state.recoveredAwaitingID) == strings.TrimSpace(awaitingID) && state.completedAt.IsZero()
}

func recoveredAwaitingRunFromManaged(ctx context.Context, state *managedRun, initialSeq int64) contracts.RecoveredAwaitingRun {
	if state == nil {
		return contracts.RecoveredAwaitingRun{}
	}
	return contracts.RecoveredAwaitingRun{
		Context:    ctx,
		Control:    state.control,
		Run:        state.run,
		EventBus:   state.eventBus,
		AwaitingID: state.recoveredAwaitingID,
		InitialSeq: initialSeq,
	}
}

func (m *Manager) Submit(req api.SubmitRequest) contracts.SubmitAck {
	control, ok := m.lookupControl(req.RunID)
	if !ok {
		return contracts.SubmitAck{Accepted: false, Status: "unmatched", SubmitID: req.SubmitID, Detail: "No active run found"}
	}
	return control.ResolveSubmit(req)
}

func (m *Manager) LookupAwaiting(runID string, awaitingID string) (contracts.AwaitingSubmitContext, bool) {
	control, ok := m.lookupControl(runID)
	if !ok {
		return contracts.AwaitingSubmitContext{}, false
	}
	return control.LookupAwaiting(awaitingID)
}

func (m *Manager) ActiveAwaitings(runID string) []contracts.AwaitingSubmitContext {
	control, ok := m.lookupControl(runID)
	if !ok {
		return nil
	}
	return control.ActiveAwaitings()
}

func (m *Manager) LookupResolvedSubmit(runID string, awaitingID string) (contracts.SubmitAck, bool) {
	control, ok := m.lookupControl(runID)
	if !ok {
		return contracts.SubmitAck{}, false
	}
	return control.LookupResolvedSubmit(awaitingID)
}

func (m *Manager) Steer(req api.SteerRequest) contracts.SteerAck {
	control, ok := m.lookupControl(req.RunID)
	steerID := normalizeSteerID(req.SteerID)
	if !ok {
		return contracts.SteerAck{Accepted: false, Status: "unmatched", SteerID: steerID, Detail: "No active run found"}
	}
	req.SteerID = steerID
	if !control.EnqueueSteer(req) {
		return contracts.SteerAck{Accepted: false, Status: "unmatched", SteerID: steerID, Detail: "Run is no longer accepting steer"}
	}
	return contracts.SteerAck{Accepted: true, Status: "accepted", SteerID: steerID, Detail: "Steer accepted"}
}

func (m *Manager) Interrupt(req api.InterruptRequest) contracts.InterruptAck {
	m.mu.Lock()
	state, ok := m.runs[req.RunID]
	m.mu.Unlock()
	if !ok {
		return contracts.InterruptAck{Accepted: false, Status: "unmatched", Detail: "No active run found"}
	}
	info := contracts.InterruptInfoFromRequest(req)
	if strings.TrimSpace(info.ChatID) == "" {
		info.ChatID = state.run.ChatID
	}
	if !state.control.Interrupt(info) {
		return contracts.InterruptAck{Accepted: false, Status: "unmatched", Detail: "Run is no longer active"}
	}
	return contracts.InterruptAck{Accepted: true, Status: "accepted", Detail: "Interrupt accepted"}
}

func (m *Manager) UpdateAccessLevel(req api.AccessLevelRequest) contracts.AccessLevelAck {
	state, ok := m.lookupRun(req.RunID)
	if !ok {
		return contracts.AccessLevelAck{Accepted: false, Status: "unmatched", Detail: "No active run found"}
	}
	if strings.TrimSpace(req.AgentKey) != "" && strings.TrimSpace(state.run.AgentKey) != strings.TrimSpace(req.AgentKey) {
		return contracts.AccessLevelAck{Accepted: false, Status: "forbidden", Detail: "agentKey does not match run"}
	}
	if state.control == nil || state.control.Interrupted() || state.control.Finished() || !state.completedAt.IsZero() {
		return contracts.AccessLevelAck{Accepted: false, Status: "unmatched", Detail: "Run is no longer active"}
	}
	previous, current, version, changed := state.control.UpdateAccessLevel(req.AccessLevel)
	status := "updated"
	detail := "accessLevel updated"
	if !changed {
		status = "unchanged"
		detail = "accessLevel unchanged"
	}
	ack := contracts.AccessLevelAck{
		Accepted:            true,
		Status:              status,
		PreviousAccessLevel: previous,
		AccessLevel:         current,
		Version:             version,
		Detail:              detail,
	}
	if changed && state.eventBus != nil {
		state.eventBus.Publish(stream.EventData{
			Seq:       state.eventBus.LatestSeq() + 1,
			Type:      "run.access_level.changed",
			Timestamp: time.Now().UnixMilli(),
			Payload: map[string]any{
				"runId":               state.run.RunID,
				"previousAccessLevel": previous,
				"accessLevel":         current,
				"version":             version,
				"reason":              strings.TrimSpace(req.Reason),
			},
		})
	}
	return ack
}

func (m *Manager) Finish(runID string) {
	m.mu.Lock()
	state, ok := m.runs[runID]
	if ok {
		state.completedAt = time.Now()
	}
	m.mu.Unlock()
	if ok {
		state.control.Finish()
		if state.runEnvironment != nil {
			state.runEnvironment.Destroy()
		}
	}
}

func (m *Manager) RunEnvironment(runID string) (*runenv.Scope, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.runs[strings.TrimSpace(runID)]
	if state == nil || state.runEnvironment == nil || !state.completedAt.IsZero() {
		return nil, false
	}
	return state.runEnvironment, true
}

func (m *Manager) AttachObserver(runID string, afterSeq int64) (*stream.Observer, error) {
	state, ok := m.lookupRun(runID)
	if !ok {
		return nil, contracts.ErrRunControlUnavailable
	}
	return state.eventBus.Subscribe(afterSeq)
}

func (m *Manager) DetachObserver(runID string, observerID string) {
	state, ok := m.lookupRun(runID)
	if !ok || state.eventBus == nil {
		return
	}
	state.eventBus.Unsubscribe(observerID)
}

func (m *Manager) RunStatus(runID string) (contracts.RunStatusInfo, bool) {
	if m == nil {
		return contracts.RunStatusInfo{}, false
	}
	m.mu.Lock()
	state := m.runs[strings.TrimSpace(runID)]
	if state == nil {
		m.mu.Unlock()
		return contracts.RunStatusInfo{}, false
	}
	run := state.run
	control := state.control
	eventBus := state.eventBus
	startedAt := state.startedAt
	completedAt := state.completedAt
	runOrigin := cloneRunOrigin(state.runOrigin)
	m.mu.Unlock()
	info := contracts.RunStatusInfo{
		RunID:             run.RunID,
		ChatID:            run.ChatID,
		AgentKey:          run.AgentKey,
		TeamID:            run.TeamID,
		ExecutionAgentKey: run.ExecutionAgentKey,
		State:             control.State(),
		LastSeq:           eventBus.LatestSeq(),
		OldestSeq:         eventBus.OldestSeq(),
		ObserverCount:     eventBus.ObserverCount(),
		StartedAt:         startedAt.UnixMilli(),
		RunOrigin:         runOrigin,
		EditingMode:       run.EditingMode,
	}
	info.AccessLevel, info.AccessLevelVersion = control.AccessLevelSnapshot()
	if !completedAt.IsZero() {
		info.CompletedAt = completedAt.UnixMilli()
	}
	return info, true
}

func (m *Manager) ActiveRunForChat(chatID string) (contracts.RunStatusInfo, bool, error) {
	if strings.TrimSpace(chatID) == "" {
		return contracts.RunStatusInfo{}, false, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	chatKey := strings.TrimSpace(chatID)
	match, runIDs := m.activeRunMatchLocked(chatKey)

	if len(runIDs) == 0 || match == nil {
		return contracts.RunStatusInfo{}, false, nil
	}
	if len(runIDs) > 1 {
		return contracts.RunStatusInfo{}, false, &contracts.ActiveRunConflictError{
			ChatID: chatKey,
			RunIDs: append([]string(nil), runIDs...),
		}
	}

	return runStatusInfoFromManagedRun(match), true, nil
}

func (m *Manager) RequestCompactForChat(chatID string, req contracts.CompactControlRequest) (contracts.ActiveRunCompactAck, error) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return contracts.ActiveRunCompactAck{Status: "unmatched"}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	match, runIDs := m.activeRunMatchLocked(chatID)
	if len(runIDs) > 1 {
		return contracts.ActiveRunCompactAck{}, &contracts.ActiveRunConflictError{ChatID: chatID, RunIDs: append([]string(nil), runIDs...)}
	}
	if len(runIDs) == 0 || match == nil || match.control == nil {
		return contracts.ActiveRunCompactAck{Status: "unmatched"}, nil
	}
	ack := contracts.ActiveRunCompactAck{Run: runStatusInfoFromManagedRun(match)}
	if !match.control.ContextCompactSupported() {
		ack.Status = "unsupported"
		return ack, nil
	}
	req.ChatID = chatID
	handle, status := match.control.EnqueueCompact(req)
	ack.Handle = handle
	ack.Status = status
	return ack, nil
}

func (m *Manager) RouteCompactForChat(req contracts.CompactControlRequest) (contracts.ActiveRunCompactAck, error) {
	chatID := strings.TrimSpace(req.ChatID)
	requestID := strings.TrimSpace(req.RequestID)
	if chatID == "" || requestID == "" {
		return contracts.ActiveRunCompactAck{Status: "invalid"}, nil
	}
	key := chatID + "\x00" + requestID
	m.mu.Lock()
	defer m.mu.Unlock()
	match, runIDs := m.activeRunMatchLocked(chatID)
	if len(runIDs) > 1 {
		return contracts.ActiveRunCompactAck{}, &contracts.ActiveRunConflictError{ChatID: chatID, RunIDs: append([]string(nil), runIDs...)}
	}
	if len(runIDs) == 1 && match != nil && match.control != nil {
		ack := contracts.ActiveRunCompactAck{Run: runStatusInfoFromManagedRun(match)}
		if !match.control.ContextCompactSupported() {
			ack.Status = "unsupported"
			return ack, nil
		}
		req.ChatID = chatID
		ack.Handle, ack.Status = match.control.EnqueueCompact(req)
		return ack, nil
	}
	if completed := m.chatMaintenanceResult[key]; completed.Valid() {
		return contracts.ActiveRunCompactAck{Handle: completed, Status: "completed"}, nil
	}
	if pending := m.chatMaintenance[chatID]; pending.Valid() {
		if pending.Request().RequestID == requestID {
			return contracts.ActiveRunCompactAck{Handle: pending, Status: "history_joined"}, nil
		}
		return contracts.ActiveRunCompactAck{Status: "busy"}, nil
	}
	if len(m.chatQueryAdmissions[chatID]) > 0 {
		return contracts.ActiveRunCompactAck{Status: "busy"}, nil
	}
	handle := contracts.NewCompactControlHandle(req)
	m.chatMaintenance[chatID] = handle
	return contracts.ActiveRunCompactAck{Handle: handle, Status: "history_acquired"}, nil
}

func (m *Manager) CompleteChatMaintenance(chatID string, requestID string, result api.CompactResponse) {
	chatID = strings.TrimSpace(chatID)
	requestID = strings.TrimSpace(requestID)
	key := chatID + "\x00" + requestID
	m.mu.Lock()
	handle := m.chatMaintenance[chatID]
	if !handle.Valid() || handle.Request().RequestID != requestID {
		m.mu.Unlock()
		return
	}
	delete(m.chatMaintenance, chatID)
	m.chatMaintenanceResult[key] = handle
	m.mu.Unlock()
	handle.Complete(result)
}

func (m *Manager) activeRunMatchLocked(chatID string) (*managedRun, []string) {
	var (
		match  *managedRun
		runIDs []string
	)
	for _, state := range m.runs {
		if state == nil || state.eventBus == nil || !state.completedAt.IsZero() {
			continue
		}
		if activeRunScopeID(state.run) != chatID {
			continue
		}
		runIDs = append(runIDs, state.run.RunID)
		if match == nil || state.startedAt.After(match.startedAt) {
			match = state
		}
	}
	return match, runIDs
}

func querySessionRunScopeID(session contracts.QuerySession) string {
	if scopeID := strings.TrimSpace(session.RunScopeID); scopeID != "" {
		return scopeID
	}
	return strings.TrimSpace(session.ChatID)
}

func activeRunScopeID(run contracts.ActiveRun) string {
	if scopeID := strings.TrimSpace(run.ScopeID); scopeID != "" {
		return scopeID
	}
	return strings.TrimSpace(run.ChatID)
}

func runStatusInfoFromManagedRun(state *managedRun) contracts.RunStatusInfo {
	if state == nil {
		return contracts.RunStatusInfo{}
	}
	info := contracts.RunStatusInfo{
		RunID:             state.run.RunID,
		ChatID:            state.run.ChatID,
		AgentKey:          state.run.AgentKey,
		TeamID:            state.run.TeamID,
		ExecutionAgentKey: state.run.ExecutionAgentKey,
		State:             state.control.State(),
		LastSeq:           state.eventBus.LatestSeq(),
		OldestSeq:         state.eventBus.OldestSeq(),
		ObserverCount:     state.eventBus.ObserverCount(),
		StartedAt:         state.startedAt.UnixMilli(),
		RunOrigin:         cloneRunOrigin(state.runOrigin),
		EditingMode:       state.run.EditingMode,
	}
	info.AccessLevel, info.AccessLevelVersion = state.control.AccessLevelSnapshot()
	if !state.completedAt.IsZero() {
		info.CompletedAt = state.completedAt.UnixMilli()
	}
	return info
}

func cloneRunOrigin(origin *contracts.RunOrigin) *contracts.RunOrigin {
	if origin == nil {
		return nil
	}
	cloned := *origin
	return &cloned
}

func normalizeSteerID(steerID string) string {
	if steerID != "" {
		return steerID
	}
	return time.Now().UTC().Format("20060102150405.000000000")
}

func (m *Manager) EventBus(runID string) (*stream.RunEventBus, bool) {
	state, ok := m.lookupRun(runID)
	if !ok || state.eventBus == nil {
		return nil, false
	}
	return state.eventBus, true
}

func (m *Manager) startReaper() {
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

func (m *Manager) reapExpiredRuns() {
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
		activeSince := state.startedAt
		if state.reaperStartOverride && !state.activeSince.IsZero() {
			activeSince = state.activeSince
		}
		if m.maxBackgroundDuration > 0 && now.Sub(activeSince) > m.maxBackgroundDuration {
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
				Type:      "run.error",
				Timestamp: time.Now().UnixMilli(),
				Payload: map[string]any{
					"runId": state.run.RunID,
					"error": map[string]any{
						"code":     "expired",
						"message":  "run expired",
						"scope":    "run",
						"category": "runtime",
					},
				},
			})
		}
		if !state.control.Interrupt(contracts.InterruptInfo{
			Source: contracts.InterruptSourceReaper,
			Reason: contracts.InterruptReasonRunExpired,
			Detail: "run exceeded max background duration",
			ChatID: state.run.ChatID,
		}) {
			log.Printf("[runctl] reaper skip interrupt run=%s state=%s", state.run.RunID, state.control.State())
		}
	}
}

func (m *Manager) lookupControl(runID string) (*contracts.RunControl, bool) {
	state, ok := m.lookupRun(runID)
	if !ok {
		return nil, false
	}
	return state.control, true
}

func (m *Manager) lookupRun(runID string) (*managedRun, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.runs[runID]
	return state, ok
}

func IsRunInterrupted(err error) bool {
	return errors.Is(err, contracts.ErrRunInterrupted)
}

var (
	_ contracts.RunManager                  = (*Manager)(nil)
	_ contracts.ChatQueryAdmissionService   = (*Manager)(nil)
	_ contracts.ExclusiveRunRegistrar       = (*Manager)(nil)
	_ contracts.RecoveredAwaitingRunService = (*Manager)(nil)
	_ contracts.ActiveRunCompactService     = (*Manager)(nil)
	_ contracts.ChatCompactCoordinator      = (*Manager)(nil)
)
