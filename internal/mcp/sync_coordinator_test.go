package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestSyncCoordinatorStartReturnsBeforeBlockedInitialSync(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	transport := &blockingMCPRoundTripper{started: requestStarted, canceled: requestCanceled}

	root := t.TempDir()
	writeMCPRegistryFile(t, filepath.Join(root, "blocked.yml"), "serverKey: blocked\nbaseUrl: https://blocked.example.test\nendpointPath: /mcp\nstartup-timeout: 30\nretry: 1\n")
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	gate := NewAvailabilityGate()
	client := NewClientWithGate(registry, &http.Client{Transport: transport}, gate)
	defer client.Close()
	syncer := NewToolSync(registry, client)
	if status, ok := syncer.ServerStatus("blocked"); !ok || status.Status != ToolSyncStatusPending {
		t.Fatalf("initial status = %#v, found=%v", status, ok)
	}

	ctx, cancel := context.WithCancel(context.Background())
	coordinator := NewSyncCoordinator(registry, syncer, gate, time.Hour)
	startedAt := time.Now()
	coordinator.Start(ctx)
	if elapsed := time.Since(startedAt); elapsed >= 500*time.Millisecond {
		cancel()
		t.Fatalf("Start blocked for %s", elapsed)
	}
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("background MCP request did not start")
	}
	if status, ok := syncer.ServerStatus("blocked"); !ok || status.Status != ToolSyncStatusSyncing {
		cancel()
		t.Fatalf("syncing status = %#v, found=%v", status, ok)
	}
	cancel()
	select {
	case <-requestCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("canceling the coordinator did not cancel the MCP request")
	}
}

type blockingMCPRoundTripper struct {
	started  chan struct{}
	canceled chan struct{}
	start    sync.Once
	cancel   sync.Once
}

func (t *blockingMCPRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodDelete {
		return nil, errors.New("test transport rejects session cleanup")
	}
	t.start.Do(func() { close(t.started) })
	<-req.Context().Done()
	t.cancel.Do(func() { close(t.canceled) })
	return nil, req.Context().Err()
}

func TestRegistryReloaderPublishesLocalStateWithoutRemoteSync(t *testing.T) {
	server := newSDKMCPTestServer(t, "remote_tool", nil)
	defer server.Close()
	root := t.TempDir()
	path := filepath.Join(root, "demo.yml")
	writeMCPRegistryFile(t, path, "serverKey: demo\nbaseUrl: "+server.URL+"\nretry: 0\n")
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	client := NewClient(registry, server.Client())
	defer client.Close()
	syncer := NewToolSync(registry, client)
	if tools, err := syncer.Load(context.Background()); err != nil || len(tools) != 1 {
		t.Fatalf("initial sync tools=%#v err=%v", tools, err)
	}
	scheduler := &recordingSyncScheduler{}
	reloader := NewRegistryReloader(registry, syncer, scheduler)

	writeMCPRegistryFile(t, path, "serverKey: demo\nbaseUrl: http://192.0.2.1\nendpointPath: /mcp\nstartup-timeout: 30\nretry: 1\n")
	startedAt := time.Now()
	if err := reloader.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("local registry reload waited for remote MCP: %s", elapsed)
	}
	if scheduler.Calls() != 1 {
		t.Fatalf("scheduled sync calls = %d, want 1", scheduler.Calls())
	}
	if status, ok := syncer.ServerStatus("demo"); !ok || status.Status != ToolSyncStatusPending {
		t.Fatalf("changed server status = %#v, found=%v", status, ok)
	}
	if tools := syncer.Definitions(); len(tools) != 0 {
		t.Fatalf("changed server retained stale tools: %#v", tools)
	}

	writeMCPRegistryFile(t, path, "serverKey: demo\ntransport: websocket\nbaseUrl: http://127.0.0.1\n")
	if err := reloader.Reload(context.Background()); err == nil {
		t.Fatal("invalid local MCP config unexpectedly reloaded")
	}
	if scheduler.Calls() != 1 {
		t.Fatalf("invalid config scheduled remote work: %d", scheduler.Calls())
	}
}

func TestRegistryReloaderRetainsUnchangedToolsAndRemovesDeletedServers(t *testing.T) {
	server := newSDKMCPTestServer(t, "remote_tool", nil)
	defer server.Close()
	root := t.TempDir()
	stablePath := filepath.Join(root, "stable.yml")
	writeMCPRegistryFile(t, stablePath, "serverKey: stable\nbaseUrl: "+server.URL+"\nretry: 0\n")
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	client := NewClient(registry, server.Client())
	defer client.Close()
	syncer := NewToolSync(registry, client)
	if _, err := syncer.Load(context.Background()); err != nil {
		t.Fatalf("initial Load: %v", err)
	}
	scheduler := &recordingSyncScheduler{}
	reloader := NewRegistryReloader(registry, syncer, scheduler)

	writeMCPRegistryFile(t, filepath.Join(root, "new.yml"), "serverKey: new\nbaseUrl: http://192.0.2.1\n")
	if err := reloader.Reload(context.Background()); err != nil {
		t.Fatalf("add server Reload: %v", err)
	}
	if got := syncer.ToolNamesForServers([]string{"stable"}); !reflect.DeepEqual(got, []string{"remote_tool"}) {
		t.Fatalf("unchanged server tools = %#v", got)
	}
	if status, ok := syncer.ServerStatus("stable"); !ok || status.Status != ToolSyncStatusReady {
		t.Fatalf("unchanged server status = %#v, found=%v", status, ok)
	}
	if status, ok := syncer.ServerStatus("new"); !ok || status.Status != ToolSyncStatusPending {
		t.Fatalf("new server status = %#v, found=%v", status, ok)
	}

	if err := os.Remove(stablePath); err != nil {
		t.Fatalf("remove stable registry: %v", err)
	}
	if err := reloader.Reload(context.Background()); err != nil {
		t.Fatalf("delete server Reload: %v", err)
	}
	if _, ok := syncer.ServerStatus("stable"); ok {
		t.Fatal("deleted server retained sync status")
	}
	if tools := syncer.Definitions(); len(tools) != 0 {
		t.Fatalf("deleted server retained tools: %#v", tools)
	}
}

func TestSyncCoordinatorCancelsObsoleteAttemptAndSyncsLatestRegistry(t *testing.T) {
	readyServer := newSDKMCPTestServer(t, "latest_tool", nil)
	defer readyServer.Close()
	blockedStarted := make(chan struct{})
	blockedCanceled := make(chan struct{})
	blockedTransport := &blockingMCPRoundTripper{started: blockedStarted, canceled: blockedCanceled}
	readyTransport := readyServer.Client().Transport
	router := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "blocked.example.test" {
			return blockedTransport.RoundTrip(req)
		}
		return readyTransport.RoundTrip(req)
	})

	root := t.TempDir()
	path := filepath.Join(root, "demo.yml")
	writeMCPRegistryFile(t, path, "serverKey: demo\nbaseUrl: https://blocked.example.test\nstartup-timeout: 30\nretry: 0\n")
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	gate := NewAvailabilityGate()
	client := NewClientWithGate(registry, &http.Client{Transport: router}, gate)
	defer client.Close()
	syncer := NewToolSync(registry, client)
	notifications := &recordingMCPNotificationSink{}
	coordinator := NewSyncCoordinator(registry, syncer, gate, time.Hour, notifications)
	reloader := NewRegistryReloader(registry, syncer, coordinator)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coordinator.Start(ctx)

	select {
	case <-blockedStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("obsolete MCP request did not start")
	}
	writeMCPRegistryFile(t, path, "serverKey: demo\nbaseUrl: "+readyServer.URL+"\nretry: 0\n")
	if err := reloader.Reload(context.Background()); err != nil {
		t.Fatalf("Reload latest registry: %v", err)
	}
	select {
	case <-blockedCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("registry reload did not cancel obsolete MCP request")
	}
	waitForMCPStatus(t, syncer, "demo", ToolSyncStatusReady, 3*time.Second)
	if got := syncer.ToolNamesForServers([]string{"demo"}); !reflect.DeepEqual(got, []string{"latest_tool"}) {
		t.Fatalf("latest registry tools = %#v", got)
	}
	if notifications.count("catalog.updated") == 0 {
		t.Fatal("latest tool synchronization did not broadcast catalog.updated")
	}
}

func TestToolSyncDiscardsResultFromObsoleteRegistryVersion(t *testing.T) {
	upstream := newSDKMCPTestServer(t, "remote_tool", nil)
	defer upstream.Close()
	firstRequestStarted := make(chan struct{})
	releaseFirstRequest := make(chan struct{})
	var blockOnce sync.Once
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blocked := false
		blockOnce.Do(func() {
			blocked = true
			close(firstRequestStarted)
		})
		if blocked {
			<-releaseFirstRequest
		}
		upstream.Config.Handler.ServeHTTP(w, r)
	}))
	defer proxy.Close()

	root := t.TempDir()
	path := filepath.Join(root, "demo.yml")
	writeMCPRegistryFile(t, path, "serverKey: demo\nbaseUrl: "+proxy.URL+"\ntoolPrefix: old\nretry: 0\n")
	registry, err := NewRegistry(root)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	client := NewClient(registry, proxy.Client())
	defer client.Close()
	syncer := NewToolSync(registry, client)
	resultCh := make(chan ToolSyncResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, loadErr := syncer.refreshTools(context.Background(), nil)
		resultCh <- result
		errCh <- loadErr
	}()

	select {
	case <-firstRequestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("old registry synchronization did not start")
	}
	writeMCPRegistryFile(t, path, "serverKey: demo\nbaseUrl: "+proxy.URL+"\ntoolPrefix: latest\nretry: 0\n")
	if err := registry.Reload(); err != nil {
		t.Fatalf("Reload registry: %v", err)
	}
	syncer.ReconcileRegistry()
	close(releaseFirstRequest)
	result := <-resultCh
	if loadErr := <-errCh; loadErr != nil {
		t.Fatalf("obsolete sync returned error: %v", loadErr)
	}
	if !result.Stale {
		t.Fatalf("obsolete sync result was not marked stale: %#v", result)
	}
	if tools := syncer.Definitions(); len(tools) != 0 {
		t.Fatalf("obsolete sync published tools: %#v", tools)
	}
	if status, ok := syncer.ServerStatus("demo"); !ok || status.Status != ToolSyncStatusPending {
		t.Fatalf("latest registry status = %#v, found=%v", status, ok)
	}
}

type recordingSyncScheduler struct {
	mu    sync.Mutex
	calls int
}

func (s *recordingSyncScheduler) ScheduleSync() {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
}

func (s *recordingSyncScheduler) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func waitForMCPStatus(t *testing.T, syncer *ToolSync, serverKey string, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if status, ok := syncer.ServerStatus(serverKey); ok && status.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	status, ok := syncer.ServerStatus(serverKey)
	t.Fatalf("server %q status = %#v, found=%v, want %q", serverKey, status, ok, want)
}
