package reload

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform-runner-go/internal/config"
)

type recordingCatalogReloader struct {
	reasons chan string
}

func (r recordingCatalogReloader) Reload(_ context.Context, reason string) error {
	r.reasons <- reason
	return nil
}

func TestStartBackgroundReloadersIgnoresDSStoreChanges(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}

	reasons := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartBackgroundReloaders(ctx, config.Config{
		Paths: config.PathsConfig{
			AgentsDir: agentsDir,
		},
	}, recordingCatalogReloader{reasons: reasons})

	time.Sleep(100 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(agentsDir, ".DS_Store"), []byte("finder"), 0o644); err != nil {
		t.Fatalf("write .DS_Store: %v", err)
	}
	assertNoReloadReason(t, reasons, reloadDebounce+300*time.Millisecond)

	if err := os.WriteFile(filepath.Join(agentsDir, "demo.yml"), []byte("key: demo\n"), 0o644); err != nil {
		t.Fatalf("write runtime file: %v", err)
	}
	assertReloadReason(t, reasons, "agents", 2*time.Second)
}

func assertNoReloadReason(t *testing.T, reasons <-chan string, timeout time.Duration) {
	t.Helper()
	select {
	case reason := <-reasons:
		t.Fatalf("unexpected reload reason %q", reason)
	case <-time.After(timeout):
	}
}

func assertReloadReason(t *testing.T, reasons <-chan string, want string, timeout time.Duration) {
	t.Helper()
	select {
	case reason := <-reasons:
		if reason != want {
			t.Fatalf("reload reason = %q, want %q", reason, want)
		}
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for reload reason %q", want)
	}
}
