package stream

import (
	"fmt"
	"sync"
	"sync/atomic"
)

const defaultObserverBuffer = 64

type ReplayWindowExceededError struct {
	AfterSeq  int64
	OldestSeq int64
	LatestSeq int64
}

type ObserverLimitExceededError struct {
	Max int
}

func (e *ObserverLimitExceededError) Error() string {
	if e == nil || e.Max <= 0 {
		return "observer limit exceeded"
	}
	return fmt.Sprintf("observer limit exceeded: max=%d", e.Max)
}

func (e *ReplayWindowExceededError) Error() string {
	if e == nil {
		return "replay window exceeded"
	}
	return fmt.Sprintf("replay window exceeded: afterSeq=%d oldestSeq=%d latestSeq=%d", e.AfterSeq, e.OldestSeq, e.LatestSeq)
}

type Observer struct {
	ID     string
	Events <-chan EventData

	live     bool
	ch       chan EventData
	once     sync.Once
	done     chan struct{}
	doneOnce sync.Once
}

func (o *Observer) closeCh() {
	if o == nil {
		return
	}
	o.once.Do(func() {
		close(o.ch)
	})
}

func (o *Observer) MarkDone() {
	if o == nil || o.done == nil {
		return
	}
	o.doneOnce.Do(func() {
		close(o.done)
	})
}

func (o *Observer) Done() <-chan struct{} {
	if o == nil || o.done == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return o.done
}

type RunEventBus struct {
	mu                    sync.RWMutex
	events                []EventData
	frozen                bool
	observers             map[string]*Observer
	maxEvents             int
	maxObservers          int
	oldestSeq             int64
	latestSeq             int64
	nextObserverID        atomic.Int64
	onObserverCountChange func(int)
}

func NewRunEventBus(maxEvents int, maxObservers int, onObserverCountChange func(int)) *RunEventBus {
	if maxEvents <= 0 {
		maxEvents = 10000
	}
	return &RunEventBus{
		observers:             map[string]*Observer{},
		maxEvents:             maxEvents,
		maxObservers:          maxObservers,
		onObserverCountChange: onObserverCountChange,
	}
}

func (b *RunEventBus) Publish(event EventData) {
	if b == nil {
		return
	}

	var (
		droppedIDs    []string
		droppedObs    []*Observer
		onCountChange func(int)
	)

	b.mu.Lock()
	if b.frozen {
		b.mu.Unlock()
		return
	}
	b.events = append(b.events, event)
	b.latestSeq = event.Seq
	if len(b.events) == 1 || b.oldestSeq == 0 {
		b.oldestSeq = event.Seq
	}
	if b.maxEvents > 0 && len(b.events) > b.maxEvents {
		trim := len(b.events) - b.maxEvents
		b.events = append([]EventData(nil), b.events[trim:]...)
		if len(b.events) > 0 {
			b.oldestSeq = b.events[0].Seq
		} else {
			b.oldestSeq = 0
		}
	}
	for id, observer := range b.observers {
		select {
		case observer.ch <- event:
		default:
			droppedIDs = append(droppedIDs, id)
			droppedObs = append(droppedObs, observer)
		}
	}
	if len(droppedIDs) > 0 {
		for _, id := range droppedIDs {
			delete(b.observers, id)
		}
		onCountChange = b.onObserverCountChange
	}
	observerCount := len(b.observers)
	b.mu.Unlock()

	for _, observer := range droppedObs {
		observer.closeCh()
		observer.MarkDone()
	}
	if onCountChange != nil {
		onCountChange(observerCount)
	}
}

func (b *RunEventBus) Subscribe(afterSeq int64) (*Observer, error) {
	if b == nil {
		return nil, fmt.Errorf("event bus unavailable")
	}

	b.mu.Lock()
	if b.maxObservers > 0 && len(b.observers) >= b.maxObservers && !b.frozen {
		err := &ObserverLimitExceededError{Max: b.maxObservers}
		b.mu.Unlock()
		return nil, err
	}
	if b.oldestSeq > 0 && afterSeq < b.oldestSeq-1 {
		err := &ReplayWindowExceededError{
			AfterSeq:  afterSeq,
			OldestSeq: b.oldestSeq,
			LatestSeq: b.latestSeq,
		}
		b.mu.Unlock()
		return nil, err
	}

	replay := make([]EventData, 0, len(b.events))
	for _, event := range b.events {
		if event.Seq > afterSeq {
			replay = append(replay, event)
		}
	}

	bufferSize := defaultObserverBuffer
	if len(replay) > bufferSize {
		bufferSize = len(replay) + defaultObserverBuffer
	}

	id := fmt.Sprintf("obs-%d", b.nextObserverID.Add(1))
	observer := &Observer{
		ID:   id,
		ch:   make(chan EventData, bufferSize),
		live: !b.frozen,
		done: make(chan struct{}),
	}
	observer.Events = observer.ch
	for _, event := range replay {
		observer.ch <- event
	}

	observerCount := len(b.observers)
	if !b.frozen {
		b.observers[id] = observer
		observerCount = len(b.observers)
	}
	frozen := b.frozen
	onCountChange := b.onObserverCountChange
	b.mu.Unlock()

	if !frozen && onCountChange != nil {
		onCountChange(observerCount)
	}
	if frozen {
		observer.closeCh()
		observer.MarkDone()
	}
	return observer, nil
}

func (b *RunEventBus) Unsubscribe(observerID string) {
	if b == nil || observerID == "" {
		return
	}

	var (
		observer      *Observer
		observerCount int
		onCountChange func(int)
	)

	b.mu.Lock()
	observer = b.observers[observerID]
	if observer != nil {
		delete(b.observers, observerID)
		observerCount = len(b.observers)
		onCountChange = b.onObserverCountChange
	}
	b.mu.Unlock()

	if observer == nil {
		return
	}
	observer.closeCh()
	observer.MarkDone()
	if onCountChange != nil {
		onCountChange(observerCount)
	}
}

func (b *RunEventBus) Freeze() {
	if b == nil {
		return
	}

	var (
		observers     []*Observer
		onCountChange func(int)
	)

	b.mu.Lock()
	if b.frozen {
		b.mu.Unlock()
		return
	}
	b.frozen = true
	observers = make([]*Observer, 0, len(b.observers))
	for _, observer := range b.observers {
		observers = append(observers, observer)
	}
	b.observers = map[string]*Observer{}
	onCountChange = b.onObserverCountChange
	b.mu.Unlock()

	for _, observer := range observers {
		observer.closeCh()
	}
	if onCountChange != nil {
		onCountChange(0)
	}
}

func (b *RunEventBus) FreezeAndWait() {
	if b == nil {
		return
	}

	var (
		observers     []*Observer
		onCountChange func(int)
	)

	b.mu.Lock()
	if b.frozen {
		b.mu.Unlock()
		return
	}
	b.frozen = true
	observers = make([]*Observer, 0, len(b.observers))
	for _, observer := range b.observers {
		observers = append(observers, observer)
	}
	b.observers = map[string]*Observer{}
	onCountChange = b.onObserverCountChange
	b.mu.Unlock()

	for _, observer := range observers {
		observer.closeCh()
	}
	for _, observer := range observers {
		<-observer.Done()
	}
	if onCountChange != nil {
		onCountChange(0)
	}
}

func (b *RunEventBus) ObserverCount() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.observers)
}

func (b *RunEventBus) OldestSeq() int64 {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.oldestSeq
}

func (b *RunEventBus) LatestSeq() int64 {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.latestSeq
}

func (b *RunEventBus) Frozen() bool {
	if b == nil {
		return true
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.frozen
}
