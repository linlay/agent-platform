package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedDesktopWithoutBuiltinCache(t *testing.T) {
	s := Sources{ExternalRoot: filepath.Join(t.TempDir(), "connectors-center")}
	pkg, release, err := s.InstallEmbeddedDesktop()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !pkg.Builtin || len(pkg.NativeTools()) != 2 || len(pkg.Skills) != 2 {
		t.Fatalf("incomplete Desktop: %#v", pkg)
	}
	if filepath.Dir(pkg.Dir) != filepath.Join(s.SharedRoot(), "builtin.desktop") {
		t.Fatal(pkg.Dir)
	}
	for _, skill := range pkg.Skills {
		for _, name := range []string{"SKILL.md", "references"} {
			if _, err := os.Stat(filepath.Join(skill.Dir, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	s.NativeDesktopDir = pkg.Dir
	items, err := s.LoadAll()
	if err != nil || len(items) != 1 {
		t.Fatalf("catalog: %v %v", items, err)
	}
	file, err := s.ReadFile("builtin.desktop", "native.json")
	if err != nil || !strings.Contains(file.Content, "desktop") {
		t.Fatalf("read: %+v %v", file, err)
	}
	// Even another process/source without our NativeDesktopDir must respect the lease.
	if err := (Sources{ExternalRoot: s.ExternalRoot}).CollectShared(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pkg.Dir); err != nil {
		t.Fatal(err)
	}
	second, releaseSecond, err := s.InstallEmbeddedDesktop()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	if second.Dir != pkg.Dir {
		t.Fatalf("not reused: %s != %s", second.Dir, pkg.Dir)
	}
	entries, err := os.ReadDir(filepath.Dir(pkg.Dir))
	if err != nil {
		t.Fatal(err)
	}
	versions := 0
	for _, e := range entries {
		if e.IsDir() && validDigest(e.Name()) {
			versions++
		}
	}
	if versions != 1 {
		t.Fatalf("versions: %d", versions)
	}
	// Old cache content is never selected or even loaded as the native source.
	s.BuiltinRoot = t.TempDir()
	if err := os.Mkdir(filepath.Join(s.BuiltinRoot, "builtin.desktop"), 0755); err != nil {
		t.Fatal(err)
	}
	items, err = s.LoadAll()
	if err != nil || len(items) != 1 || items[0].Dir != pkg.Dir {
		t.Fatalf("old cache overrides embedded: %v %v", items, err)
	}
	if err := os.WriteFile(filepath.Join(pkg.Dir, "skills", "desktop-action", "SKILL.md"), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, release, err := s.InstallEmbeddedDesktop(); err == nil {
		release()
		t.Fatal("accepted corrupt shared package")
	}
}
