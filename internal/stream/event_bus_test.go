package stream

import (
	"sync"
	"testing"
	"time"
)

func TestRunEventBusReplaysAndFreezes(t *testing.T) {
	bus := NewRunEventBus(10, 0, nil)
	bus.Publish(EventData{Seq: 1, Type: "request.query"})
	bus.Publish(EventData{Seq: 2, Type: "run.start"})
	bus.Publish(EventData{Seq: 3, Type: "content.delta"})

	observer, err := bus.Subscribe(1)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	first := <-observer.Events
	if first.Seq != 2 {
		t.Fatalf("expected replay seq=2, got %#v", first)
	}
	second := <-observer.Events
	if second.Seq != 3 {
		t.Fatalf("expected replay seq=3, got %#v", second)
	}

	bus.Freeze()
	if _, ok := <-observer.Events; ok {
		t.Fatalf("expected observer channel to close after freeze")
	}
}

func TestRunEventBusDropsSlowObserver(t *testing.T) {
	bus := NewRunEventBus(256, 0, nil)
	observer, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	_ = observer

	for seq := int64(1); seq <= int64(defaultObserverBuffer); seq++ {
		bus.Publish(EventData{Seq: seq, Type: "content.delta"})
		if got := bus.ObserverCount(); got != 1 {
			t.Fatalf("expected observer to remain attached before buffer fills, got count=%d at seq=%d", got, seq)
		}
	}
	bus.Publish(EventData{Seq: int64(defaultObserverBuffer + 1), Type: "content.delta"})

	if got := bus.ObserverCount(); got != 0 {
		t.Fatalf("expected slow observer to be dropped, got count=%d", got)
	}
}

func TestRunEventBusReplayExpansionDoesNotDropObserver(t *testing.T) {
	bus := NewRunEventBus(256, 0, nil)
	for seq := int64(1); seq <= int64(defaultObserverBuffer+1); seq++ {
		bus.Publish(EventData{Seq: seq, Type: "content.delta"})
	}

	observer, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer bus.Unsubscribe(observer.ID)

	for seq := int64(1); seq <= int64(defaultObserverBuffer+1); seq++ {
		event, ok := <-observer.Events
		if !ok {
			t.Fatalf("expected replay event seq=%d before channel close", seq)
		}
		if event.Seq != seq {
			t.Fatalf("expected replay seq=%d, got %#v", seq, event)
		}
	}
	if got := bus.ObserverCount(); got != 1 {
		t.Fatalf("expected observer to remain attached after large replay, got count=%d", got)
	}
}

func TestRunEventBusCloseIsIdempotent(t *testing.T) {
	bus := NewRunEventBus(256, 0, nil)
	observer, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	bus.Freeze()
	bus.Unsubscribe(observer.ID)
	observer.closeCh()
}

func TestRunEventBusFreezeAndWaitBlocksUntilObserverDone(t *testing.T) {
	bus := NewRunEventBus(16, 0, nil)
	observer, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	release := make(chan struct{})
	go func() {
		for range observer.Events {
		}
		<-release
		observer.MarkDone()
	}()

	freezeDone := make(chan struct{})
	go func() {
		bus.FreezeAndWait()
		close(freezeDone)
	}()

	select {
	case <-freezeDone:
		t.Fatalf("expected FreezeAndWait to block until observer completion")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)

	select {
	case <-freezeDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for FreezeAndWait to return")
	}
}

func TestRunEventBusFreezeAndWaitIgnoresUnsubscribedObserver(t *testing.T) {
	bus := NewRunEventBus(16, 0, nil)
	observer, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	bus.Unsubscribe(observer.ID)

	done := make(chan struct{})
	go func() {
		bus.FreezeAndWait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for FreezeAndWait after unsubscribe")
	}
}

func TestRunEventBusFreezeAndWaitIgnoresDroppedObserver(t *testing.T) {
	bus := NewRunEventBus(256, 0, nil)
	observer, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	_ = observer

	for seq := int64(1); seq <= int64(defaultObserverBuffer)+1; seq++ {
		bus.Publish(EventData{Seq: seq, Type: "content.delta"})
	}

	done := make(chan struct{})
	go func() {
		bus.FreezeAndWait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for FreezeAndWait after slow observer drop")
	}
}

func TestRunEventBusReplayWindowExceeded(t *testing.T) {
	bus := NewRunEventBus(3, 0, nil)
	for seq := int64(1); seq <= 4; seq++ {
		bus.Publish(EventData{Seq: seq, Type: "content.delta"})
	}

	_, err := bus.Subscribe(0)
	if err == nil {
		t.Fatalf("expected replay window error")
	}
	replayErr, ok := err.(*ReplayWindowExceededError)
	if !ok {
		t.Fatalf("expected ReplayWindowExceededError, got %T", err)
	}
	if replayErr.OldestSeq != 2 || replayErr.LatestSeq != 4 {
		t.Fatalf("unexpected replay window: %#v", replayErr)
	}
}

func TestRunEventBusRejectsObserverLimit(t *testing.T) {
	bus := NewRunEventBus(10, 1, nil)
	first, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe first observer: %v", err)
	}
	defer bus.Unsubscribe(first.ID)

	_, err = bus.Subscribe(0)
	if err == nil {
		t.Fatalf("expected observer limit error")
	}
	if _, ok := err.(*ObserverLimitExceededError); !ok {
		t.Fatalf("expected ObserverLimitExceededError, got %T", err)
	}
}

func TestRunEventBusConcurrentPublishSubscribe(t *testing.T) {
	bus := NewRunEventBus(512, 0, nil)
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			observer, err := bus.Subscribe(0)
			if err != nil {
				t.Errorf("subscribe: %v", err)
				return
			}
			defer bus.Unsubscribe(observer.ID)
			for range observer.Events {
			}
		}()
	}
	for i := 0; i < 100; i++ {
		bus.Publish(EventData{Seq: int64(i + 1), Type: "content.delta"})
	}
	bus.Freeze()
	wg.Wait()
}
