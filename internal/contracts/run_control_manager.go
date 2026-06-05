package contracts

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/stream"
)

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
	control.SetInitialAccessLevel(session.AccessLevel)
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
		return SubmitAck{Accepted: false, Status: "unmatched", SubmitID: req.SubmitID, Detail: "No active run found"}
	}
	return control.ResolveSubmit(req)
}

func (m *InMemoryRunManager) LookupAwaiting(runID string, awaitingID string) (AwaitingSubmitContext, bool) {
	control, ok := m.lookupControl(runID)
	if !ok {
		return AwaitingSubmitContext{}, false
	}
	return control.LookupAwaiting(awaitingID)
}

func (m *InMemoryRunManager) LookupResolvedSubmit(runID string, awaitingID string) (SubmitAck, bool) {
	control, ok := m.lookupControl(runID)
	if !ok {
		return SubmitAck{}, false
	}
	return control.LookupResolvedSubmit(awaitingID)
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

func (m *InMemoryRunManager) UpdateAccessLevel(req api.AccessLevelRequest) AccessLevelAck {
	state, ok := m.lookupRun(req.RunID)
	if !ok {
		return AccessLevelAck{Accepted: false, Status: "unmatched", Detail: "No active run found"}
	}
	if strings.TrimSpace(req.AgentKey) != "" && strings.TrimSpace(state.run.AgentKey) != strings.TrimSpace(req.AgentKey) {
		return AccessLevelAck{Accepted: false, Status: "forbidden", Detail: "agentKey does not match run"}
	}
	if state.control == nil || state.control.Interrupted() || state.control.Finished() || !state.completedAt.IsZero() {
		return AccessLevelAck{Accepted: false, Status: "unmatched", Detail: "Run is no longer active"}
	}
	previous, current, version, changed := state.control.UpdateAccessLevel(req.AccessLevel)
	status := "updated"
	detail := "accessLevel updated"
	if !changed {
		status = "unchanged"
		detail = "accessLevel unchanged"
	}
	ack := AccessLevelAck{
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
	info.AccessLevel, info.AccessLevelVersion = state.control.AccessLevelSnapshot()
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
	info.AccessLevel, info.AccessLevelVersion = match.control.AccessLevelSnapshot()
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
		if state.control != nil && state.control.HasNoTimeoutAwaiting() {
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
