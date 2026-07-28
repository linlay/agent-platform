package kbase

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"agent-platform/internal/contracts"
)

type editingHookRefreshResolver struct {
	cfg resolvedConfig
}

func (r editingHookRefreshResolver) Resolve(string) (resolvedConfig, *Embedder, error) {
	return r.cfg, nil, nil
}

type editingHookGeneration struct {
	options RefreshOptions
}

func (g *editingHookGeneration) Refresh(_ context.Context, _ resolvedConfig, _ *Embedder, options RefreshOptions, _ func() int) (RefreshResult, error) {
	g.options = options
	return RefreshResult{
		AgentKey: "docs", Status: "ready", Scope: "delta",
		ChangedFiles: 1, IndexedChunks: 4,
	}, nil
}

func (*editingHookGeneration) Rollback(context.Context, resolvedConfig, string) (*Generation, error) {
	return nil, nil
}

func (*editingHookGeneration) ReleaseStorageGeneration(string, string) {}

func TestEditingHookSynchronouslyRequestsDeltaRefresh(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "policy.md")
	if err := os.WriteFile(path, []byte("policy"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := testKBaseAgent("docs", root, "runtime")
	manager := NewManager(ManagerOptions{RuntimeDir: t.TempDir()}, stubAgentSource{agents: map[string]AgentSpec{"docs": spec}}, nil)
	backend := &editingHookGeneration{}
	manager.refresh = newRefreshCoordinator(editingHookRefreshResolver{cfg: resolvedConfig{
		AgentKey: "docs", WorkspaceRoot: root, StorageDir: t.TempDir(),
	}}, manager.state, backend)

	result := manager.AfterFileChange(context.Background(), contracts.FileChangeEvent{
		AgentKey: "docs", ChatID: "chat", RunID: "run", WorkspaceRoot: root, FilePath: path,
		PreviousContentSHA256: "before", ContentSHA256: "after",
	})
	if result.Name != editingIndexHookName || result.Status != "success" || result.FilePath != "policy.md" {
		t.Fatalf("unexpected successful hook result: %#v", result)
	}
	if !reflect.DeepEqual(backend.options, RefreshOptions{Mode: "editing", Scope: "delta", Paths: []string{"policy.md"}}) {
		t.Fatalf("unexpected refresh options: %#v", backend.options)
	}
	if result.Data["scope"] != "delta" || result.Data["changedFiles"] != 1 || result.Data["indexedChunks"] != 4 {
		t.Fatalf("unexpected hook data: %#v", result.Data)
	}
}

func TestEditingHookSkipsPathExcludedByKBaseConfig(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "private", "policy.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("policy"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := testKBaseAgent("docs", root, "runtime")
	spec.Config.Include = []string{"public/**/*.md"}
	manager := NewManager(ManagerOptions{RuntimeDir: t.TempDir()}, stubAgentSource{agents: map[string]AgentSpec{"docs": spec}}, nil)

	result := manager.AfterFileChange(context.Background(), contracts.FileChangeEvent{
		AgentKey: "docs", FilePath: path, ContentSHA256: "after",
	})
	if result.Name != editingIndexHookName || result.Status != "skipped" ||
		result.Reason != "excluded_by_kbase_config" || result.FilePath != "private/policy.md" {
		t.Fatalf("unexpected excluded hook result: %#v", result)
	}
}

func TestEditingHookReportsIndexFailureWithoutChangingWriteStatus(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "policy.md")
	if err := os.WriteFile(path, []byte("policy"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := testKBaseAgent("docs", root, "runtime")
	manager := NewManager(ManagerOptions{RuntimeDir: t.TempDir()}, stubAgentSource{agents: map[string]AgentSpec{"docs": spec}}, nil)

	result := manager.AfterFileChange(context.Background(), contracts.FileChangeEvent{
		AgentKey: "docs", ChatID: "chat", RunID: "run", FilePath: path,
		PreviousContentSHA256: "before", ContentSHA256: "after",
	})
	if result.Name != editingIndexHookName || result.Status != "failed" || result.Message == "" {
		t.Fatalf("unexpected failed hook result: %#v", result)
	}
	if err := manager.state.DegradedError("docs"); err == nil {
		t.Fatal("editing index failure must mark the capability degraded until a refresh succeeds")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "policy" {
		t.Fatalf("hook failure must not roll back the file: %q err=%v", string(data), err)
	}
}

func TestEditingHookIgnoresChatspaceMutation(t *testing.T) {
	root := t.TempDir()
	chatDir := t.TempDir()
	path := filepath.Join(chatDir, "report.txt")
	if err := os.WriteFile(path, []byte("temporary"), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := testKBaseAgent("docs", root, "runtime")
	manager := NewManager(ManagerOptions{RuntimeDir: t.TempDir()}, stubAgentSource{agents: map[string]AgentSpec{"docs": spec}}, nil)

	result := manager.AfterFileChange(context.Background(), contracts.FileChangeEvent{
		AgentKey: "docs",
		ChatID:   "chat",
		RunID:    "run",
		FilePath: path,
	})
	if !reflect.DeepEqual(result, contracts.FileChangeHookResult{}) {
		t.Fatalf("chatspace mutation must not produce a KBASE hook result: %#v", result)
	}
	if err := manager.state.DegradedError("docs"); err != nil {
		t.Fatalf("chatspace mutation must not degrade KBASE: %v", err)
	}
}
