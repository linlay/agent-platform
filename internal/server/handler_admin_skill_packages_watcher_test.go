package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/reload"
)

func TestCatalogMutationFailureReloadsChangesMadeWhileWatcherSuspended(t *testing.T) {
	fixture := newTestFixture(t)
	probeDir := filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "suspended-probe")
	if err := os.MkdirAll(probeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloader := fixture.catalogReloader.(*reload.RuntimeCatalogReloader)
	reload.StartBackgroundReloaders(ctx, fixture.cfg, reloader)
	injected := errors.New("injected mutation failure")
	err := reloader.WithCatalogMutation(ctx, func(context.Context) error {
		// This event cannot be observed: the watcher has released its handles.
		// A failed mutation must still reconcile unrelated changes on resume.
		if err := os.WriteFile(filepath.Join(probeDir, "SKILL.md"), []byte("---\nname: suspended-probe\ndescription: Changed while suspended\n---\n\nProbe.\n"), 0o644); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("mutation error = %v, want injected failure", err)
	}
	awaitWatchedSkillDescription(t, fixture.registry, "suspended-probe", "Changed while suspended")
}

type failingOnceWatchedRegistry struct {
	catalog.Registry
	fail atomic.Bool
}

func (r *failingOnceWatchedRegistry) Reload(ctx context.Context, reason string) error {
	if reason == "skills" && r.fail.CompareAndSwap(true, false) {
		return errors.New("injected catalog publication failure")
	}
	return r.Registry.Reload(ctx, reason)
}

func TestAdminSkillPackageWatcherRestoresAfterPublicationRollback(t *testing.T) {
	fixture := newTestFixture(t)
	registry := &failingOnceWatchedRegistry{Registry: fixture.registry}
	reloader := reload.NewRuntimeCatalogReloader(registry, fixture.modelRegistry, nil, nil, "", nil)
	fixture.server.deps.CatalogReloader = reloader
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload.StartBackgroundReloaders(ctx, fixture.cfg, reloader)
	importVersion := func(version string) *httptest.ResponseRecorder {
		t.Helper()
		archive := serverSkillImportZIP(t, map[string]string{
			"manifest.json":                  fmt.Sprintf(`{"schemaVersion":1,"type":"skill-package","id":"rollback-watch-pack","version":%q,"skills":[{"id":"rollback-child","version":%q,"path":"skills/rollback-child/"}]}`, version, version),
			"skills/rollback-child/SKILL.md": fmt.Sprintf("---\nname: rollback-child\ndescription: Version %s\n---\n\nContent.\n", version),
		})
		request := httptest.NewRequest(http.MethodPost, "/api/admin/skill-packages/import?key=rollback-watch-pack&version="+version, bytes.NewReader(archive))
		request.Header.Set("Content-Type", "application/zip")
		recorder := httptest.NewRecorder()
		fixture.server.ServeHTTP(recorder, request)
		return recorder
	}
	if result := importVersion("1.0.0"); result.Code != http.StatusOK {
		t.Fatalf("initial install: %d %s", result.Code, result.Body.String())
	}
	registry.fail.Store(true)
	if result := importVersion("2.0.0"); result.Code == http.StatusOK {
		t.Fatalf("expected publication failure: %s", result.Body.String())
	}
	packages := getAPIData[[]api.AdminSkillPackageResponse](t, fixture.server, http.MethodGet, "/api/admin/skill-packages", nil)
	if len(packages) != 1 || packages[0].Version != "1.0.0" {
		t.Fatalf("rollback did not restore package record: %#v", packages)
	}
	path := filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "rollback-child", "SKILL.md")
	content, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(content, []byte("Version 1.0.0")) {
		t.Fatalf("rollback did not restore content: %q, %v", content, err)
	}
	// Write after the failed transaction to prove directory watches were rebuilt.
	if err := os.WriteFile(path, bytes.ReplaceAll(content, []byte("Version 1.0.0"), []byte("Edited after rollback")), 0o644); err != nil {
		t.Fatal(err)
	}
	awaitWatchedSkillDescription(t, fixture.registry, "rollback-child", "Edited after rollback")
}

func awaitWatchedSkillDescription(t *testing.T, registry catalog.Registry, key, description string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if skill, ok := registry.SkillDefinition(key); ok && skill.Description == description {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("background watcher did not publish %s description %q", key, description)
		case <-tick.C:
		}
	}
}

// Exercise the production reloader and real filesystem watches, including the
// watched-existing-directory case that prevents directory renames on Windows.
func TestAdminSkillPackageLifecycleWithBackgroundWatcher(t *testing.T) {
	fixture := newTestFixture(t)
	root := fixture.cfg.Paths.SkillsCenterDir
	probeDir := filepath.Join(root, "watch-probe")
	if err := os.MkdirAll(probeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload.StartBackgroundReloaders(ctx, fixture.cfg, fixture.catalogReloader)

	// A changed catalog value proves the live watcher actually ran; a mere
	// notification could instead have come from an earlier explicit reload.
	assertWatcherWorks := func(marker string) {
		t.Helper()
		content := fmt.Sprintf("---\nname: watch-probe\ndescription: %s\n---\n\nProbe.\n", marker)
		if err := os.WriteFile(filepath.Join(probeDir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			if skill, ok := fixture.registry.SkillDefinition("watch-probe"); ok && skill.Description == marker {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("background watcher did not load probe %q", marker)
			case <-tick.C:
			}
		}
	}
	assertWatcherWorks("before-package-import")

	importPackage := func(version string) {
		t.Helper()
		archive := serverSkillImportZIP(t, map[string]string{
			"manifest.json":                                  fmt.Sprintf(`{"schemaVersion":1,"type":"skill-package","id":"watch-pack","version":%q,"skills":[{"id":"watch-child","version":%q,"path":"skills/watch-child/"}]}`, version, version),
			"skills/watch-child/SKILL.md":                    fmt.Sprintf("---\nname: watch-child\ndescription: Watched child\nmetadata:\n  version: %s\n---\n\nPackage %s.\n", version, version),
			"skills/watch-child/references/nested/readme.md": "Nested directories must also release their watches before rename.\n",
		})
		request := httptest.NewRequest(http.MethodPost, "/api/admin/skill-packages/import?key=watch-pack&version="+version, bytes.NewReader(archive))
		request.Header.Set("Content-Type", "application/zip")
		recorder := httptest.NewRecorder()
		fixture.server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("import %s: status %d: %s", version, recorder.Code, recorder.Body.String())
		}
		packages := getAPIData[[]api.AdminSkillPackageResponse](t, fixture.server, http.MethodGet, "/api/admin/skill-packages", nil)
		if len(packages) != 1 || packages[0].Version != version || len(packages[0].Skills) != 1 {
			t.Fatalf("unexpected package state: %#v", packages)
		}
		content, err := os.ReadFile(filepath.Join(root, "watch-child", "SKILL.md"))
		if err != nil || !bytes.Contains(content, []byte("Package "+version+".")) {
			t.Fatalf("installed child does not match version %s: %q, %v", version, content, err)
		}
	}
	importPackage("1.0.0")
	assertWatcherWorks("after-first-import")
	importPackage("1.0.0")
	assertWatcherWorks("after-same-version-reinstall")
	importPackage("2.0.0")
	assertWatcherWorks("after-upgrade")

	childBody, _ := json.Marshal(api.DeleteAdminSkillPackageSkillRequest{PackageID: "watch-pack", SkillID: "watch-child"})
	child := getAPIData[api.DeleteAdminSkillPackageSkillResponse](t, fixture.server, http.MethodPost, "/api/admin/skill-packages/skills/delete", childBody)
	if !child.Deleted || !child.PackageDeleted {
		t.Fatalf("last child deletion should remove the package: %#v", child)
	}
	if _, err := os.Stat(filepath.Join(root, "watch-child")); !os.IsNotExist(err) {
		t.Fatalf("child remains after deletion: %v", err)
	}
	assertWatcherWorks("after-child-delete")
	importPackage("2.0.0")
	assertWatcherWorks("after-reimport")

	deleteBody, _ := json.Marshal(api.DeleteAdminSkillPackageRequest{Key: "watch-pack"})
	deleted := getAPIData[api.DeleteAdminSkillPackageResponse](t, fixture.server, http.MethodPost, "/api/admin/skill-packages/delete", deleteBody)
	if !deleted.Deleted || len(deleted.Skills) != 1 {
		t.Fatalf("unexpected package deletion: %#v", deleted)
	}
	for _, path := range []string{filepath.Join(root, "watch-child"), filepath.Join(root, ".package", "watch-pack.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("package deletion left %s: %v", path, err)
		}
	}
	assertWatcherWorks("after-package-delete")
}
