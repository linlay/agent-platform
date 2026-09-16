package connectorauth

import (
	"agent-platform/internal/connector"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDisconnectStopsBusinessTreeBeforePrivateCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX real subprocess; Windows taskkill path cross compiled")
	}
	root := t.TempDir()
	writeCLIPackage(t, root, "demo", simpleCLI())
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil).ForOwner("user:alice")
	pkg, _ := m.sources.Load("demo")
	yes := true
	pkg.UpdateConnection(&yes, &yes)
	private, _ := pkg.UserStateDir()
	os.MkdirAll(filepath.Join(private, "home"), 0700)
	scoped, release, err := m.beginBusiness(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(private, "home", "revived")
	cmd := exec.CommandContext(scoped, "/bin/sh", "-c", "sleep 1; mkdir -p "+quotePOSIX(filepath.Dir(marker))+"; touch "+quotePOSIX(marker))
	connector.ConfigureProcessTree(cmd)
	if err = cmd.Start(); err != nil {
		release()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); release(); close(done) }()
	if _, err = m.Disconnect(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("business process was not reaped")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("detached business process restored private state")
	}
	if _, _, err = m.beginBusiness(context.Background(), "demo"); err == nil {
		t.Fatal("new business started after disconnect")
	}
}
func TestRetiredConnectCannotRestoreBinding(t *testing.T) {
	root := t.TempDir()
	writeCLIPackage(t, root, "demo", simpleCLI())
	m := New(context.Background(), connector.Sources{ExternalRoot: root}, nil).ForOwner("user:alice")
	pkg, _ := m.sources.Load("demo")
	generation := m.epoch("demo")
	if _, err := m.Disconnect(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if err := m.markBoundAt(pkg, generation); err == nil {
		t.Fatal("late connect restored binding")
	}
	state, _ := pkg.ReadConnection()
	if state.Bound || state.Enabled {
		t.Fatal(state)
	}
}
