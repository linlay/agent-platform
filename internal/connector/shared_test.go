package connector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedCLIStateRootChangeIgnoresLegacySourceIndex(t *testing.T) {
	s := runtimeFixture(t)
	dir := filepath.Join(s.ExternalRoot, "demo")
	putRuntimeFile(t, filepath.Join(dir, "connector.json"), `{"id":"demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":null}`)
	putRuntimeFile(t, filepath.Join(dir, "cli.json"), minimalTestCLI)
	pkg, err := s.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.ManagedCLI() {
		t.Fatal("fixture must be an ordinary CLI without managed login")
	}
	first, err := s.InstallShared(pkg)
	if err != nil {
		t.Fatal(err)
	}
	// Older ordinary CLI indexes omit StateRoot. Keep one pointing at the old
	// descriptor to verify that upgrades cannot reuse it after a layout change.
	legacy, err := json.Marshal(MountReference{ID: pkg.ID, Digest: filepath.Base(first.Dir)})
	if err != nil {
		t.Fatal(err)
	}
	putRuntimeFile(t, filepath.Join(s.SharedRoot(), pkg.ID, ".source-"+first.InstallationDigest+".json"), string(legacy))
	s.StateRoot = filepath.Join(t.TempDir(), "new-state")
	pkg, err = s.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.InstallShared(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Dir == first.Dir {
		t.Fatal("state root change reused a descriptor from the previous root")
	}
	var descriptor struct{ StateDir, InstallationDigest string }
	if err := ReadJSON(filepath.Join(second.Dir, ".platform-runtime.json"), &descriptor); err != nil {
		t.Fatal(err)
	}
	if descriptor.StateDir != filepath.Join(s.StateRoot, pkg.ID) {
		t.Fatalf("descriptor state directory: %q", descriptor.StateDir)
	}
	again, err := s.InstallShared(pkg)
	if err != nil || again.Dir != second.Dir {
		t.Fatalf("same state root did not reuse its shared package: %v", err)
	}
	if digest, err := RuntimeFingerprint(pkg.Dir); err != nil || digest != first.InstallationDigest {
		t.Fatalf("shared installation changed the source package: %v", err)
	}
}

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
	next, err := s.InstallShared(pkg)
	if err != nil || next.Dir != mounted.Dir {
		t.Fatalf("startup failed to reuse: %v", err)
	}
}

func TestDesktopNativePackageTrustAndSkills(t *testing.T) {
	s := runtimeFixture(t)
	if err := WriteBuiltin(filepath.Join(s.BuiltinRoot, "builtin.platform-control"), "platform-control", ""); err != nil {
		t.Fatal(err)
	}
	pkg, err := s.Load("builtin.platform-control")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.NativeTools()) != 11 || len(pkg.Skills) != 1 || pkg.BinDir != "" || !pkg.Builtin {
		t.Fatalf("native contract: %#v", pkg)
	}
	icon, err := pkg.ReadIcon()
	if err != nil || icon.MediaType != "image/svg+xml" {
		t.Fatalf("desktop connector SVG icon: %q, %v", icon.MediaType, err)
	}
	for _, skill := range pkg.Skills {
		data, err := os.ReadFile(filepath.Join(skill.Dir, "assets", "icon.svg"))
		if err != nil || ValidateIconSVG(data) != nil {
			t.Fatalf("desktop skill SVG icon %s: %v", skill.Name, err)
		}
	}
	for _, name := range []string{"platform-control", "desktop-cdp", "web-control"} {
		if !IsReservedSkill(name) {
			t.Fatal("native skill selectable as ordinary skill")
		}
	}
	putRuntimeFile(t, filepath.Join(s.ExternalRoot, "evil", "connector.json"), `{"id":"evil","name":"Evil","version":"1.0.0","type":"native","auth_mode":null}`)
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
