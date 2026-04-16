package stream

import (
	"testing"
)

func TestRunEventBusReplaysAndFreezes(t *testing.T) {
	bus := NewRunEventBus(10, nil)
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
	bus := NewRunEventBus(256, nil)
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
	bus := NewRunEventBus(256, nil)
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
	bus := NewRunEventBus(256, nil)
	observer, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	bus.Freeze()
	bus.Unsubscribe(observer.ID)
	observer.closeCh()
}

func TestRunEventBusReplayWindowExceeded(t *testing.T) {
	bus := NewRunEventBus(3, nil)
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
