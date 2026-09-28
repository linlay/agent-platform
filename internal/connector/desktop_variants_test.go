package connector

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedDesktopVariants(t *testing.T) {
	s := Sources{ExternalRoot: filepath.Join(t.TempDir(), "connectors-center")}
	full, releaseFull, err := s.InstallEmbeddedDesktop()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFull()
	web, releaseWeb, err := s.InstallEmbeddedDesktopWeb()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseWeb()
	s.NativeDesktopDir, s.NativeDesktopWebDir = full.Dir, web.Dir
	items, err := s.LoadAll()
	if err != nil || len(items) != 2 {
		t.Fatalf("catalog: %v %v", items, err)
	}
	if !reflect.DeepEqual(full.NativeTools(), web.NativeTools()) || len(web.Skills) != 2 || web.AuthMode != AuthNoAuth {
		t.Fatalf("web capabilities: %+v", web)
	}
	// Both registered IDs prefer the leased binary resources over stale caches.
	s.BuiltinRoot = t.TempDir()
	for _, pkg := range []Package{full, web} {
		if err := os.Mkdir(filepath.Join(s.BuiltinRoot, pkg.ID), 0755); err != nil {
			t.Fatal(err)
		}
	}
	items, err = s.LoadAll()
	if err != nil || len(items) != 2 || items[0].Dir != full.Dir || items[1].Dir != web.Dir {
		t.Fatalf("stale cache override: %v %v", items, err)
	}
	// Shared CDP and page contracts are byte-identical and links stay inside each package.
	links := regexp.MustCompile(`\[[^\]]*\]\(([^)#]+)(?:#[^)]*)?\)`)
	for _, pkg := range []Package{full, web} {
		err := filepath.WalkDir(pkg.Dir, func(file string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			rel, err := filepath.Rel(pkg.Dir, file)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if pkg.ID == DesktopWebConnectorID {
				own := rel == "connector.json" || rel == "skills/desktop-action/SKILL.md" || rel == "skills/desktop-action/references/catalog.md"
				if !own && !desktopWebSharedResource(rel) {
					t.Errorf("unexpected web resource: %s", rel)
				}
				if !own {
					other, err := os.ReadFile(filepath.Join(full.Dir, filepath.FromSlash(rel)))
					if err != nil || !bytes.Equal(data, other) {
						t.Errorf("shared resource drift: %s %v", rel, err)
					}
				}
			}
			if strings.HasSuffix(file, ".md") {
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
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(web.Dir, "skills", "desktop-action", "references", "catalog.md"))
	if err != nil {
		t.Fatal(err)
	}
	actions := regexp.MustCompile("(?m)^\\| `([^`]+)` ").FindAllSubmatch(data, -1)
	if len(actions) != 21 {
		t.Fatalf("unexpected web action count: %d", len(actions))
	}
	for _, action := range actions {
		name := string(action[1])
		if !strings.HasPrefix(name, "desktop.web.") && !strings.HasPrefix(name, "desktop.workpanel.") {
			t.Errorf("web catalog exposes %s", name)
		}
	}
	// Both versions stay pinned even when a collector has no embedded source fields.
	if err := (Sources{ExternalRoot: s.ExternalRoot}).CollectShared(); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []Package{full, web} {
		if _, err := os.Stat(pkg.Dir); err != nil {
			t.Fatal(err)
		}
	}
	second, release, err := s.InstallEmbeddedDesktopWeb()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if second.Dir != web.Dir {
		t.Fatal("web package was not reused")
	}
}

func TestDesktopPresentationAndSelection(t *testing.T) {
	for _, tc := range []struct{ name, zh, en string }{{"desktop", "桌面端", "Desktop"}, {"desktop-web", "桌面端（网页）", "Desktop (Web)"}} {
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
	if ValidateDesktopSelection([]string{DesktopConnectorID, DesktopWebConnectorID}) != ErrDesktopVariantConflict {
		t.Fatal("accepted both variants")
	}
	if err := ValidateDesktopSelection([]string{"other", DesktopWebConnectorID}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifest("builtin.impostor", []byte(`{"id":"builtin.impostor","name":"impostor","version":"1.0.0","type":"native","auth_mode":"no_auth"}`)); err == nil {
		t.Fatal("accepted unregistered native package")
	}
}
