package kbase

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

type coordinatorTestResolver map[string]resolvedConfig

func (r coordinatorTestResolver) Resolve(agentKey string) (resolvedConfig, *Embedder, error) {
	cfg, ok := r[agentKey]
	if !ok {
		return resolvedConfig{}, nil, fmt.Errorf("missing agent %s", agentKey)
	}
	return cfg, &Embedder{}, nil
}

type coordinatorTestGeneration struct {
	entered chan string
	release <-chan struct{}

	mu        sync.Mutex
	active    int
	maxActive int
}

func (g *coordinatorTestGeneration) Refresh(_ context.Context, cfg resolvedConfig, _ *Embedder, options RefreshOptions, pendingChanges func() int) (RefreshResult, error) {
	g.mu.Lock()
	g.active++
	if g.active > g.maxActive {
		g.maxActive = g.active
	}
	g.mu.Unlock()
	g.entered <- cfg.AgentKey
	<-g.release
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
	return RefreshResult{AgentKey: cfg.AgentKey, Mode: options.Mode, Status: "success", PendingChanges: pendingChangeCount(pendingChanges)}, nil
}

func (*coordinatorTestGeneration) Rollback(context.Context, resolvedConfig, string) (*Generation, error) {
	return &Generation{}, nil
}

func (*coordinatorTestGeneration) ReleaseStorageGeneration(string, string) {}

func (g *coordinatorTestGeneration) maximumActive() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxActive
}

func TestRefreshCoordinatorSerializesCanonicalStorage(t *testing.T) {
	release := make(chan struct{})
	backend := &coordinatorTestGeneration{entered: make(chan string, 2), release: release}
	coordinator := newRefreshCoordinator(coordinatorTestResolver{
		"one": {AgentKey: "one", StorageDir: "/tmp/shared-kbase"},
		"two": {AgentKey: "two", StorageDir: "/tmp/shared-kbase"},
	}, newCapabilityState(), backend)

	done := make(chan error, 2)
	go func() {
		_, err := coordinator.Refresh(context.Background(), "one", RefreshOptions{Mode: "test"})
		done <- err
	}()
	if got := <-backend.entered; got != "one" {
		t.Fatalf("first refresh = %q", got)
	}
	go func() {
		_, err := coordinator.Refresh(context.Background(), "two", RefreshOptions{Mode: "test"})
		done <- err
	}()
	select {
	case got := <-backend.entered:
		t.Fatalf("same-storage refresh entered concurrently: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if got := <-backend.entered; got != "two" {
		t.Fatalf("second refresh = %q", got)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := backend.maximumActive(); got != 1 {
		t.Fatalf("max active refreshes = %d, want 1", got)
	}
}

func TestRefreshCoordinatorAllowsDifferentStoragesInParallel(t *testing.T) {
	release := make(chan struct{})
	backend := &coordinatorTestGeneration{entered: make(chan string, 2), release: release}
	coordinator := newRefreshCoordinator(coordinatorTestResolver{
		"one": {AgentKey: "one", StorageDir: "/tmp/kbase-one"},
		"two": {AgentKey: "two", StorageDir: "/tmp/kbase-two"},
	}, newCapabilityState(), backend)

	done := make(chan error, 2)
	for _, key := range []string{"one", "two"} {
		key := key
		go func() {
			_, err := coordinator.Refresh(context.Background(), key, RefreshOptions{Mode: "test"})
			done <- err
		}()
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case key := <-backend.entered:
			seen[key] = true
		case <-time.After(time.Second):
			t.Fatal("different-storage refreshes did not enter in parallel")
		}
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if !seen["one"] || !seen["two"] || backend.maximumActive() != 2 {
		t.Fatalf("parallel refreshes seen=%v maxActive=%d", seen, backend.maximumActive())
	}
}

func TestRefreshCoordinatorRejectsWorkAfterCloseBegins(t *testing.T) {
	release := make(chan struct{})
	close(release)
	backend := &coordinatorTestGeneration{entered: make(chan string, 1), release: release}
	coordinator := newRefreshCoordinator(coordinatorTestResolver{
		"docs": {AgentKey: "docs", StorageDir: "/tmp/kbase-docs"},
	}, newCapabilityState(), backend)
	coordinator.BeginClose()

	result, err := coordinator.Refresh(context.Background(), "docs", RefreshOptions{Mode: "manual"})
	if KindOf(err) != ErrorUnavailable || result.Status != "failed" {
		t.Fatalf("closed refresh result=%#v err=%v", result, err)
	}
	select {
	case key := <-backend.entered:
		t.Fatalf("closed coordinator invoked backend for %s", key)
	default:
	}
}

type coordinatorRollbackGeneration struct {
	rollbackEntered chan struct{}
	rollbackRelease chan struct{}
	refreshEntered  chan struct{}
}

func (g *coordinatorRollbackGeneration) Refresh(context.Context, resolvedConfig, *Embedder, RefreshOptions, func() int) (RefreshResult, error) {
	g.refreshEntered <- struct{}{}
	return RefreshResult{Status: "success"}, nil
}

func (g *coordinatorRollbackGeneration) Rollback(context.Context, resolvedConfig, string) (*Generation, error) {
	g.rollbackEntered <- struct{}{}
	<-g.rollbackRelease
	return &Generation{ID: "previous"}, nil
}

func (*coordinatorRollbackGeneration) ReleaseStorageGeneration(string, string) {}

func TestRefreshCoordinatorSerializesRollbackWithRefresh(t *testing.T) {
	backend := &coordinatorRollbackGeneration{
		rollbackEntered: make(chan struct{}, 1), rollbackRelease: make(chan struct{}), refreshEntered: make(chan struct{}, 1),
	}
	coordinator := newRefreshCoordinator(coordinatorTestResolver{
		"docs": {AgentKey: "docs", StorageDir: "/tmp/kbase-rollback"},
	}, newCapabilityState(), backend)
	rollbackDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Rollback(context.Background(), "docs", "previous")
		rollbackDone <- err
	}()
	<-backend.rollbackEntered
	refreshDone := make(chan error, 1)
	go func() {
		_, err := coordinator.Refresh(context.Background(), "docs", RefreshOptions{Mode: "manual"})
		refreshDone <- err
	}()
	select {
	case <-backend.refreshEntered:
		t.Fatal("refresh entered while rollback held the storage lock")
	case <-time.After(50 * time.Millisecond):
	}
	close(backend.rollbackRelease)
	if err := <-rollbackDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.refreshEntered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not resume after rollback released the storage lock")
	}
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
}
