package kbase

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

type lifecycleEvents struct {
	mu     sync.Mutex
	values []string
}

func (e *lifecycleEvents) add(value string) {
	e.mu.Lock()
	e.values = append(e.values, value)
	e.mu.Unlock()
}

func (e *lifecycleEvents) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.values...)
}

type lifecycleTestResolver struct{}

func (lifecycleTestResolver) Keys() []string    { return nil }
func (lifecycleTestResolver) HasRequired() bool { return false }

type lifecycleTestWatchers struct{ events *lifecycleEvents }

func (w lifecycleTestWatchers) Start(context.Context)     { w.events.add("watchers.start") }
func (w lifecycleTestWatchers) Reconcile(context.Context) { w.events.add("watchers.reconcile") }
func (w lifecycleTestWatchers) Stop()                     { w.events.add("watchers.stop") }

type lifecycleTestRefresh struct{ events *lifecycleEvents }

func (r lifecycleTestRefresh) Refresh(context.Context, string, RefreshOptions) (RefreshResult, error) {
	r.events.add("refresh.run")
	return RefreshResult{}, nil
}
func (r lifecycleTestRefresh) BeginClose()                { r.events.add("refresh.begin-close") }
func (r lifecycleTestRefresh) Wait(context.Context) error { r.events.add("refresh.wait"); return nil }

type lifecycleTestLance struct{ events *lifecycleEvents }

func (l lifecycleTestLance) SetLifecycleContext(context.Context) { l.events.add("lance.context") }
func (l lifecycleTestLance) Probe(context.Context, bool) (bool, LanceEngineState, error) {
	return false, LanceEngineState{}, nil
}
func (l lifecycleTestLance) Stop(context.Context) error { l.events.add("lance.stop"); return nil }

type lifecycleTestAuditor struct{ events *lifecycleEvents }

func (a lifecycleTestAuditor) Audit() ([]OrphanStorage, error) {
	a.events.add("audit")
	return nil, nil
}

func TestLifecycleSupervisorStartsAndClosesComponentsInOrder(t *testing.T) {
	events := &lifecycleEvents{}
	supervisor := newLifecycleSupervisor(
		time.Hour,
		lifecycleTestResolver{},
		lifecycleTestWatchers{events: events},
		lifecycleTestRefresh{events: events},
		lifecycleTestLance{events: events},
		lifecycleTestAuditor{events: events},
	)
	ctx, cancel := context.WithCancel(context.Background())
	supervisor.Start(ctx)
	cancel()
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"lance.context", "watchers.start", "audit",
		"refresh.begin-close", "watchers.stop", "refresh.wait", "lance.stop",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle order = %v, want %v", got, want)
	}
}
