package connector

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedNativeConnectors(t *testing.T) {
	s := Sources{ExternalRoot: filepath.Join(t.TempDir(), "connectors-center")}
	desktop, releaseDesktop, err := s.InstallEmbeddedPlatformControl()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseDesktop()
	web, releaseWeb, err := s.InstallEmbeddedWebControl()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseWeb()
	s.NativePlatformControlDir, s.NativeWebControlDir = desktop.Dir, web.Dir
	items, err := s.LoadAll()
	if err != nil || len(items) != 2 {
		t.Fatalf("catalog: %v %v", items, err)
	}
	// The two connectors own disjoint tools and can be mounted together.
	if len(desktop.NativeTools()) != 12 || len(desktop.Skills) != 1 || desktop.Skills[0].Name != "platform-control" {
		t.Fatalf("desktop capabilities: %v %+v", desktop.NativeTools(), desktop.Skills)
	}
	if len(web.NativeTools()) != 15 || len(web.Skills) != 1 || web.Skills[0].Name != "web-control" || web.AuthMode != AuthNoAuth {
		t.Fatalf("web-control capabilities: %v %+v", web.NativeTools(), web.Skills)
	}
	for _, tool := range web.NativeTools() {
		if id, ok := NativeToolConnector(tool); !ok || id != WebControlConnectorID {
			t.Errorf("tool %s owner = %q", tool, id)
		}
	}
	if err := ValidateSelection([]Package{desktop, web}); err != nil {
		t.Fatalf("connectors must be mountable together: %v", err)
	}
	// Both registered IDs prefer the leased binary resources over stale caches.
	s.BuiltinRoot = t.TempDir()
	for _, pkg := range []Package{desktop, web} {
		if err := os.Mkdir(filepath.Join(s.BuiltinRoot, pkg.ID), 0755); err != nil {
			t.Fatal(err)
		}
	}
	items, err = s.LoadAll()
	if err != nil || len(items) != 2 || items[0].Dir != desktop.Dir || items[1].Dir != web.Dir {
		t.Fatalf("stale cache override: %v %v", items, err)
	}
	// Skill links stay inside each package, and neither package documents the
	// tools that were replaced by the typed web-control tools.
	links := regexp.MustCompile(`\[[^\]]*\]\(([^)#]+)(?:#[^)]*)?\)`)
	for _, pkg := range []Package{desktop, web} {
		err := filepath.WalkDir(pkg.Dir, func(file string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			rel, err := filepath.Rel(pkg.Dir, file)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !strings.HasSuffix(file, ".md") {
				return nil
			}
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte("desktop_cdp ")) || bytes.Contains(data, []byte("`desktop_cdp`")) || bytes.Contains(data, []byte("desktop-cdp")) {
				t.Errorf("%s/%s still documents the removed CDP tool", pkg.ID, rel)
			}
			for _, match := range links.FindAllSubmatch(data, -1) {
				target := string(match[1])
				if strings.Contains(target, "://") {
					continue
				}
				resolved := filepath.Clean(filepath.Join(filepath.Dir(file), filepath.FromSlash(target)))
				if !strings.HasPrefix(resolved, pkg.Dir+string(filepath.Separator)) {
					t.Errorf("link escapes package: %s -> %s", rel, target)
				}
				if _, err := os.Stat(resolved); err != nil {
					t.Errorf("broken link: %s -> %s: %v", rel, target, err)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(desktop.Dir, "skills", "platform-control", "references", "catalog.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range regexp.MustCompile("(?m)^\\| `([^`]+)` ").FindAllSubmatch(data, -1) {
		name := string(action[1])
		if strings.HasPrefix(name, "desktop.workpanel.") || (strings.HasPrefix(name, "desktop.web.") && name != "desktop.web.exportArtifact") {
			t.Errorf("desktop catalog exposes web-control action %s", name)
		}
	}
	// Both versions stay pinned even when a collector has no embedded source fields.
	if err := (Sources{ExternalRoot: s.ExternalRoot}).CollectShared(); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []Package{desktop, web} {
		if _, err := os.Stat(pkg.Dir); err != nil {
			t.Fatal(err)
		}
	}
	second, release, err := s.InstallEmbeddedWebControl()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if second.Dir != web.Dir {
		t.Fatal("web-control package was not reused")
	}
}

func TestNativeConnectorPresentation(t *testing.T) {
	for _, tc := range []struct{ name, zh, en string }{{"platform-control", "平台控制", "Platform Control"}, {"web-control", "网页控制", "Web Control"}} {
		dir := t.TempDir()
		if err := WriteBuiltin(dir, tc.name, ""); err != nil {
			t.Fatal(err)
		}
		pkg, err := LoadDirectory(dir, "builtin."+tc.name)
		if err != nil {
			t.Fatal(err)
		}
		for _, locale := range []string{"zh", "zh_CN", "zh-CN"} {
			got := pkg.Manifest.Localized(locale)
			if got.Name != tc.zh || got.Description == pkg.Description || got.I18N != nil {
				t.Fatalf("localized: %+v", got)
			}
		}
		if got := pkg.Manifest.Localized("en-US"); got.Name != tc.en {
			t.Fatal(got)
		}
		if got := pkg.Manifest.Localized("fr"); got.Name != tc.en {
			t.Fatal(got)
		}
		if len(pkg.I18N) != 2 || pkg.Name != tc.en {
			t.Fatal("localization mutated source")
		}
	}
	if err := ValidateManifest("builtin.impostor", []byte(`{"id":"builtin.impostor","name":"impostor","version":"1.0.0","type":"native","auth_mode":"no_auth"}`)); err == nil {
		t.Fatal("accepted unregistered native package")
	}
}
