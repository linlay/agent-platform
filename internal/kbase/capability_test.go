package kbase

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAgentConfigCapabilityFields(t *testing.T) {
	cfg, err := ParseAgentConfig(map[string]any{
		"enabled": true,
		"source":  map[string]any{"root": " ./knowledge "},
	})
	if err != nil {
		t.Fatalf("parse capability config: %v", err)
	}
	if !cfg.Enabled || !cfg.EnabledSet || cfg.Source.Root != "./knowledge" {
		t.Fatalf("unexpected capability fields: %#v", cfg)
	}

	disabled, err := ParseAgentConfig(map[string]any{"enabled": false})
	if err != nil {
		t.Fatalf("parse disabled config: %v", err)
	}
	if disabled.Enabled || !disabled.EnabledSet {
		t.Fatalf("explicit false was not retained: %#v", disabled)
	}

	for _, raw := range []map[string]any{
		{"enabled": "true"},
		{"source": "./knowledge"},
		{"source": map[string]any{"root": 42}},
	} {
		if _, err := ParseAgentConfig(raw); err == nil {
			t.Fatalf("invalid capability config accepted: %#v", raw)
		}
	}
}

func TestResolveSourceRootPolicy(t *testing.T) {
	agentDir := t.TempDir()
	resolved, err := ResolveSourceRoot("./knowledge", agentDir)
	if err != nil {
		t.Fatalf("resolve directory source: %v", err)
	}
	if resolved != filepath.Join(agentDir, "knowledge") {
		t.Fatalf("resolved source = %q", resolved)
	}

	for _, test := range []struct {
		root     string
		agentDir string
		want     string
	}{
		{root: "", agentDir: agentDir, want: "is required"},
		{root: "@chat", agentDir: agentDir, want: "must not be"},
		{root: ".", agentDir: "", want: "only supported for directory agents"},
		{root: string(filepath.Separator), agentDir: agentDir, want: "filesystem root"},
	} {
		if _, err := ResolveSourceRoot(test.root, test.agentDir); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("ResolveSourceRoot(%q) error = %v, want %q", test.root, err, test.want)
		}
	}
}

func TestManagerOnlyOwnsEnabledCapabilities(t *testing.T) {
	root := t.TempDir()
	enabledRoot := filepath.Join(root, "enabled")
	disabledRoot := filepath.Join(root, "disabled")
	for _, path := range []string{enabledRoot, disabledRoot} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	enabled := testKBaseAgent("enabled", enabledRoot, "runtime")
	enabled.Requirement = RequirementOptional
	disabled := testKBaseAgent("disabled", disabledRoot, "runtime")
	disabled.Enabled = false
	source := &stubAgentSource{agents: map[string]AgentSpec{"enabled": enabled, "disabled": disabled}}
	manager := NewManager(ManagerOptions{RuntimeDir: filepath.Join(root, "runtime")}, source, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.ReconcileWatchers(ctx)
	manager.mu.Lock()
	_, enabledWatching := manager.watchers["enabled"]
	_, disabledWatching := manager.watchers["disabled"]
	manager.mu.Unlock()
	if !enabledWatching || disabledWatching {
		t.Fatalf("watchers enabled=%v disabled=%v", enabledWatching, disabledWatching)
	}
	if err := manager.ValidateAgent("disabled"); KindOf(err) != ErrorDisabled {
		t.Fatalf("disabled admission error = %v (%q)", err, KindOf(err))
	}

	probeCtx, probeCancel := context.WithCancel(context.Background())
	probeCancel()
	required, _, _ := manager.ProbeSidecar(probeCtx)
	if required {
		t.Fatal("optional capability must not make sidecar health required")
	}
	enabled.Requirement = RequirementRequired
	source.agents["enabled"] = enabled
	required, _, _ = manager.ProbeSidecar(probeCtx)
	if !required {
		t.Fatal("dedicated KBASE capability must make sidecar health required")
	}
}

func TestOptionalStartupStorageFailureReportsDegradedStatus(t *testing.T) {
	root := t.TempDir()
	spec := testKBaseAgent("docs", filepath.Join(root, "knowledge"), "runtime")
	spec.Requirement = RequirementOptional
	source := stubAgentSource{agents: map[string]AgentSpec{"docs": spec}}
	manager := NewManager(ManagerOptions{RuntimeDir: filepath.Join(root, "runtime")}, source, nil)
	storageDir := filepath.Join(root, "runtime", "docs")
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "legacy.db"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	failures := manager.ValidateAndAdoptStartupStorageContracts()
	if failures["docs"] == nil {
		t.Fatalf("expected isolated startup failure, got %#v", failures)
	}
	status, err := manager.Status("docs")
	if err != nil {
		t.Fatalf("optional degraded status: %v", err)
	}
	if !status.Degraded || !status.Stale || status.Error == "" || status.SourceRoot != spec.SourceRoot {
		t.Fatalf("unexpected degraded status: %#v", status)
	}
	if _, err := manager.Search(context.Background(), "docs", "policy", SearchOptions{}); KindOf(err) != ErrorUnavailable {
		t.Fatalf("degraded search error = %v (%q), want unavailable", err, KindOf(err))
	}
}
