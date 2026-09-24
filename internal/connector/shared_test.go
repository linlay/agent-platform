package connector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSharedVersionLeaseCollectionAndTamper(t *testing.T) {
	s := runtimeFixture(t)
	pkg, err := s.Load("builtin.dbx")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.InstallShared(pkg)
	if err != nil {
		t.Fatal(err)
	}
	hold, err := RetainShared(first.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	again, err := s.InstallShared(pkg)
	if err != nil || again.Dir != first.Dir {
		t.Fatalf("duplicate version: %v", err)
	}
	putRuntimeFile(t, filepath.Join(pkg.Dir, "new-reference.md"), "new")
	second, err := s.InstallShared(pkg)
	if err != nil || second.Dir == first.Dir {
		t.Fatalf("update: %v", err)
	}
	holdSecond, err := RetainShared(second.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer holdSecond()
	if err := s.CollectShared(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Dir); err != nil {
		t.Fatal("active old version collected")
	}
	hold()
	if err := s.CollectShared(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Dir); !os.IsNotExist(err) {
		t.Fatal("idle old version retained")
	}
	putRuntimeFile(t, filepath.Join(second.Dir, "new-reference.md"), "tampered")
	if _, err := s.InstallShared(pkg); err == nil {
		t.Fatal("tampered shared version accepted")
	}
}

func TestSharedAssemblyPreventsCollectionAndStartupReuses(t *testing.T) {
	s := runtimeFixture(t)
	release, err := s.AssemblyLease()
	if err != nil {
		t.Fatal(err)
	}
	pkg, _ := s.Load("builtin.dbx")
	mounted, err := s.InstallShared(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CollectShared(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mounted.Dir); err != nil {
		t.Fatal("assembly candidate collected")
	}
	release()
	if err := s.RetireSharedRuntime(filepath.Dir(s.ExternalRoot)); err != nil {
		t.Fatal(err)
	}
	next, err := s.InstallShared(pkg)
	if err != nil || next.Dir != mounted.Dir {
		t.Fatalf("startup failed to reuse: %v", err)
	}
}

func TestDesktopNativePackageTrustAndSkills(t *testing.T) {
	s := runtimeFixture(t)
	if err := WriteBuiltin(filepath.Join(s.BuiltinRoot, "builtin.desktop"), "desktop", "", "darwin"); err != nil {
		t.Fatal(err)
	}
	pkg, err := s.Load("builtin.desktop")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.NativeTools()) != 2 || len(pkg.Skills) != 2 || pkg.BinDir != "" || !pkg.Builtin {
		t.Fatalf("native contract: %#v", pkg)
	}
	for _, name := range []string{"desktop-action", "desktop-cdp"} {
		if !IsReservedSkill(name) {
			t.Fatal("native skill selectable as ordinary skill")
		}
	}
	putRuntimeFile(t, filepath.Join(s.ExternalRoot, "evil", "connector.json"), `{"id":"evil","name":"Evil","version":"1.0.0","type":"native","auth_mode":null}`)
	putRuntimeFile(t, filepath.Join(s.ExternalRoot, "evil", "native.json"), `{"capabilities":["desktop.action","desktop.cdp"]}`)
	if _, err := s.Load("evil"); err == nil {
		t.Fatal("external native handler binding accepted")
	}
}

func TestConcurrentAgentsInstallOnlyOneSharedVersion(t *testing.T) {
	s := runtimeFixture(t)
	pkg, _ := s.Load("builtin.dbx")
	results := make(chan Package, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { p, err := s.InstallShared(pkg); results <- p; failures <- err }()
	}
	dir := ""
	for i := 0; i < 8; i++ {
		p := <-results
		err := <-failures
		if err != nil {
			t.Fatal(err)
		}
		if dir == "" {
			dir = p.Dir
		}
		if p.Dir != dir {
			t.Fatal("concurrent duplicate")
		}
	}
	entries, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d package versions for one content", count)
	}
}
