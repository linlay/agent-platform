package automation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/chat"
)

const (
	defaultExecutionPendingCapacity = 1024
	executionHistoryCloseTimeout    = 5 * time.Second
	executionHistoryMaxRetry        = 30 * time.Second
)

type ExecutionRunLookup func(chatID, runID string) (*chat.RunSummary, error)

type pendingExecution struct {
	item     Execution
	revision uint64
}

// ExecutionHistoryService keeps execution history completely outside the
// query critical path. Submit only mutates bounded memory; all SQLite work is
// performed by the background worker.
type ExecutionHistoryService struct {
	dir         string
	dbFileName  string
	broadcaster Broadcaster
	runLookup   ExecutionRunLookup
	capacity    int
	startupAt   int64

	mu       sync.RWMutex
	store    *ExecutionStore
	status   ExecutionHistoryStatus
	pending  map[string]pendingExecution
	order    []string
	revision uint64

	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func NewExecutionHistoryService(dir, dbFileName string, broadcaster Broadcaster, runLookup ExecutionRunLookup) *ExecutionHistoryService {
	service := &ExecutionHistoryService{
		dir:         strings.TrimSpace(dir),
		dbFileName:  strings.TrimSpace(dbFileName),
		broadcaster: broadcaster,
		runLookup:   runLookup,
		capacity:    defaultExecutionPendingCapacity,
		startupAt:   time.Now().UnixMilli(),
		status: ExecutionHistoryStatus{
			State:   ExecutionHistoryInitializing,
			Message: "automation execution history is initializing",
		},
		pending: map[string]pendingExecution{},
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go service.run()
	return service
}

func (s *ExecutionHistoryService) Status() ExecutionHistoryStatus {
	if s == nil {
		return ExecutionHistoryStatus{State: ExecutionHistoryUnavailable, Message: ErrExecutionHistoryUnavailable.Error()}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *ExecutionHistoryService) Submit(item Execution) {
	if s == nil {
		return
	}
	item = cloneExecution(item)
	item = normalizeExecution(item)
	if item.ID == "" {
		return
	}
	s.mu.Lock()
	if current, ok := s.pending[item.ID]; ok {
		currentStage := executionLifecycleStage(current.item)
		if currentStage < 3 && executionLifecycleStage(item) >= currentStage {
			s.revision++
			s.pending[item.ID] = pendingExecution{item: item, revision: s.revision}
		}
	} else {
		if len(s.pending) >= s.capacity && len(s.order) > 0 {
			evicted := s.order[0]
			s.order = s.order[1:]
			delete(s.pending, evicted)
			log.Printf("[automation] execution history pending capacity reached; evicted executionID=%s", evicted)
		}
		s.revision++
		s.pending[item.ID] = pendingExecution{item: item, revision: s.revision}
		s.order = append(s.order, item.ID)
	}
	s.mu.Unlock()
	s.signal()
}

func (s *ExecutionHistoryService) ListByAutomation(automationID string, limit, offset int) ([]Execution, int, error) {
	store, err := s.readableStore()
	if err != nil {
		return nil, 0, err
	}
	return store.ListByAutomation(automationID, limit, offset)
}

func (s *ExecutionHistoryService) LastExecution(automationID string) (*Execution, error) {
	store, err := s.readableStore()
	if err != nil {
		return nil, err
	}
	return store.LastExecution(automationID)
}

func (s *ExecutionHistoryService) GetExecution(executionID string) (*Execution, error) {
	store, err := s.readableStore()
	if err != nil {
		return nil, err
	}
	return store.GetExecution(executionID)
}

func (s *ExecutionHistoryService) WaitReady(ctx context.Context) error {
	if s == nil {
		return ErrExecutionHistoryUnavailable
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		status := s.Status()
		if status.Available {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *ExecutionHistoryService) Close() error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() { close(s.stop) })
	select {
	case <-s.done:
		return nil
	case <-time.After(executionHistoryCloseTimeout):
		return fmt.Errorf("automation execution history close timed out after %s", executionHistoryCloseTimeout)
	}
}

func (s *ExecutionHistoryService) readableStore() (*ExecutionStore, error) {
	if s == nil {
		return nil, ErrExecutionHistoryUnavailable
	}
	s.mu.RLock()
	store := s.store
	status := s.status
	s.mu.RUnlock()
	if store == nil || !status.Available {
		message := strings.TrimSpace(status.Message)
		if message == "" {
			message = ErrExecutionHistoryUnavailable.Error()
		}
		return nil, fmt.Errorf("%w: %s", ErrExecutionHistoryUnavailable, message)
	}
	return store, nil
}

func (s *ExecutionHistoryService) run() {
	defer close(s.done)
	retry := 100 * time.Millisecond
	for {
		if s.currentStore() == nil {
			if err := s.initializeStore(); err != nil {
				s.setStatus(ExecutionHistoryStatus{State: ExecutionHistoryUnavailable, Message: err.Error()})
				if !s.wait(retry) {
					return
				}
				retry = nextExecutionRetry(retry)
				continue
			}
			retry = 100 * time.Millisecond
		}

		entry, ok := s.nextPending()
		if !ok {
			if !s.wait(0) {
				s.drainAndClose()
				return
			}
			continue
		}
		store := s.currentStore()
		if store == nil {
			continue
		}
		if err := store.Upsert(entry.item); err != nil {
			s.setStatus(ExecutionHistoryStatus{Available: true, State: ExecutionHistoryDegraded, Message: err.Error()})
			if !s.wait(retry) {
				s.drainAndClose()
				return
			}
			retry = nextExecutionRetry(retry)
			continue
		}
		retry = 100 * time.Millisecond
		s.removePending(entry.item.ID, entry.revision)
		s.setStatus(ExecutionHistoryStatus{Available: true, State: ExecutionHistoryReady})
		s.broadcastPersisted(entry.item)
	}
}

func (s *ExecutionHistoryService) initializeStore() error {
	store, err := NewExecutionStore(s.dir, s.dbFileName)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.store != nil {
		s.mu.Unlock()
		_ = store.Close()
		return nil
	}
	s.store = store
	s.status = ExecutionHistoryStatus{Available: true, State: ExecutionHistoryReady}
	s.mu.Unlock()
	if backupPath := store.BackupPath(); backupPath != "" {
		log.Printf("[automation] legacy execution history backed up to %s; active V2 history starts empty", backupPath)
	}
	go s.reconcileRunning(store)
	return nil
}

func (s *ExecutionHistoryService) reconcileRunning(store *ExecutionStore) {
	if store == nil {
		return
	}
	items, err := store.ListRunning()
	if err != nil {
		log.Printf("[automation] reconcile running executions failed: %v", err)
		return
	}
	for _, item := range items {
		// The scan is asynchronous and may overlap newly-triggered executions.
		// Only records that predate this service instance can be leftovers from
		// the previous Platform process.
		if item.StartedAt >= s.startupAt {
			continue
		}
		var summary *chat.RunSummary
		if s.runLookup != nil && item.ChatID != "" && item.RunID != "" {
			summary, err = s.runLookup(item.ChatID, item.RunID)
			if err != nil {
				log.Printf("[automation] reconcile execution lookup failed executionID=%s runID=%s err=%v", item.ID, item.RunID, err)
				summary = nil
			}
		}
		if summary != nil && strings.TrimSpace(summary.FinishReason) != "" && summary.CompletedAt > 0 {
			completed := summary.CompletedAt
			completion := chat.RunCompletion{
				ChatID:          summary.ChatID,
				RunID:           summary.RunID,
				AssistantText:   summary.AssistantText,
				InitialMessage:  summary.InitialMessage,
				FinishReason:    summary.FinishReason,
				StartedAtMillis: summary.StartedAt,
				UpdatedAtMillis: completed,
				Usage:           summary.Usage,
			}
			item = completeExecution(item, &completion, "", nil)
		} else {
			item = completeExecution(item, nil, "platform restarted before execution completed", fmt.Errorf("platform restarted before execution completed"))
		}
		s.Submit(item)
	}
}

func (s *ExecutionHistoryService) currentStore() *ExecutionStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store
}

func (s *ExecutionHistoryService) setStatus(status ExecutionHistoryStatus) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

func (s *ExecutionHistoryService) nextPending() (pendingExecution, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range s.order {
		if entry, ok := s.pending[id]; ok {
			return entry, true
		}
	}
	return pendingExecution{}, false
}

func (s *ExecutionHistoryService) removePending(id string, revision uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.pending[id]
	if !ok || current.revision != revision {
		return
	}
	delete(s.pending, id)
	for i, queuedID := range s.order {
		if queuedID == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

func (s *ExecutionHistoryService) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *ExecutionHistoryService) wait(delay time.Duration) bool {
	if delay <= 0 {
		select {
		case <-s.stop:
			return false
		case <-s.wake:
			return true
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-s.stop:
		return false
	case <-s.wake:
		return true
	case <-timer.C:
		return true
	}
}

func (s *ExecutionHistoryService) drainAndClose() {
	deadline := time.Now().Add(executionHistoryCloseTimeout)
	store := s.currentStore()
	for store != nil && time.Now().Before(deadline) {
		entry, ok := s.nextPending()
		if !ok {
			break
		}
		if err := store.Upsert(entry.item); err != nil {
			break
		}
		s.removePending(entry.item.ID, entry.revision)
	}
	if store != nil {
		_ = store.Close()
	}
}

func (s *ExecutionHistoryService) broadcastPersisted(item Execution) {
	if s == nil || s.broadcaster == nil {
		return
	}
	eventType := "automation.execution.updated"
	if item.Status != ExecutionStatusRunning {
		eventType = "automation.execution.completed"
	} else if item.RunID == "" {
		eventType = "automation.execution.created"
	}
	payload := map[string]any{
		"id":           item.ID,
		"automationId": item.AutomationID,
		"status":       item.Status,
		"chatId":       item.ChatID,
		"runId":        item.RunID,
		"finishReason": item.FinishReason,
		"startedAt":    item.StartedAt,
		"completedAt":  nullableInt64(item.CompletedAt),
	}
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("[automation] execution history broadcast panic recovered: %v", recovered)
			}
		}()
		s.broadcaster.Broadcast(eventType, payload)
	}()
}

func nextExecutionRetry(current time.Duration) time.Duration {
	if current <= 0 {
		return 100 * time.Millisecond
	}
	next := current * 5
	if next > executionHistoryMaxRetry {
		return executionHistoryMaxRetry
	}
	return next
}

func executionLifecycleStage(item Execution) int {
	if item.Status != "" && item.Status != ExecutionStatusRunning {
		return 3
	}
	if item.RunID != "" || item.ChatID != "" || item.RunStartedAt != nil {
		return 2
	}
	return 1
}

func cloneExecution(item Execution) Execution {
	item.RunStartedAt = cloneExecutionInt64(item.RunStartedAt)
	item.CompletedAt = cloneExecutionInt64(item.CompletedAt)
	item.DurationMs = cloneExecutionInt64(item.DurationMs)
	return item
}

func cloneExecutionInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func completeExecution(base Execution, completion *chat.RunCompletion, errorMessage string, execErr error) Execution {
	item := cloneExecution(base)
	completedAt := time.Now().UnixMilli()
	if completion != nil {
		item.ChatID = completion.ChatID
		item.RunID = completion.RunID
		item.ResultContent = completion.AssistantText
		item.FinishReason = strings.ToLower(strings.TrimSpace(completion.FinishReason))
		if completion.StartedAtMillis > 0 {
			item.RunStartedAt = executionInt64Ptr(completion.StartedAtMillis)
		}
		if completion.UpdatedAtMillis > 0 {
			completedAt = completion.UpdatedAtMillis
		}
	}
	item.CompletedAt = executionInt64Ptr(completedAt)
	duration := completedAt - item.StartedAt
	if duration < 0 {
		duration = 0
	}
	item.DurationMs = executionInt64Ptr(duration)
	item.Error = strings.TrimSpace(errorMessage)
	if item.Error == "" && execErr != nil {
		item.Error = strings.TrimSpace(execErr.Error())
	}
	switch item.FinishReason {
	case "complete":
		item.Status = ExecutionStatusSuccess
	case "cancel":
		item.Status = ExecutionStatusCanceled
	case "error":
		item.Status = ExecutionStatusFailed
	default:
		if execErr != nil {
			if errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) {
				item.Status = ExecutionStatusCanceled
				item.FinishReason = "cancel"
			} else {
				item.Status = ExecutionStatusFailed
				item.FinishReason = "error"
			}
		} else {
			item.Status = ExecutionStatusFailed
			if item.Error == "" {
				item.Error = "query completed without a terminal finish reason"
			}
		}
	}
	return item
}

func executionInt64Ptr(value int64) *int64 {
	return &value
}
