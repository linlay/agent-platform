package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/reload"
)

func TestAdminSkillOverwriteWithWatcherPreservesOldOnFailure(t *testing.T) {
	f := newTestFixture(t)
	root := filepath.Join(f.cfg.Paths.SkillsCenterDir, "mock-skill")
	oldMetadata := []byte(`{"version":"1.0.0","marker":"original"}`)
	if err := os.WriteFile(filepath.Join(root, "skill.json"), oldMetadata, 0o644); err != nil {
		t.Fatal(err)
	}
	oldMarkdown, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	registry := &failingOnceWatchedRegistry{Registry: f.registry}
	reloader := reload.NewRuntimeCatalogReloader(registry, f.modelRegistry, nil, nil, "", nil)
	f.server.deps.CatalogReloader = reloader
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload.StartBackgroundReloaders(ctx, f.cfg, reloader)
	upload := func(archive []byte, query string) *httptest.ResponseRecorder {
		body, kind := skillImportBody(t, "mock-skill", "skill.zip", archive)
		req := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import"+query, body)
		req.Header.Set("Content-Type", kind)
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, req)
		return rec
	}
	valid := serverSkillImportZIP(t, map[string]string{"SKILL.md": "---\nname: mock-skill\ndescription: Updated skill\n---\n\nUpdated.\n", "skill.json": `{"version":"2.0.0"}`, "references/nested/new.md": "new"})
	assertOld := func() {
		t.Helper()
		for name, want := range map[string][]byte{"SKILL.md": oldMarkdown, "skill.json": oldMetadata} {
			got, err := os.ReadFile(filepath.Join(root, name))
			if err != nil || string(got) != string(want) {
				t.Fatalf("old %s lost: %q %v", name, got, err)
			}
		}
	}
	if rec := upload(valid, ""); rec.Code != http.StatusConflict {
		t.Fatalf("default overwrite changed: %d %s", rec.Code, rec.Body.String())
	}
	if rec := upload(serverSkillImportZIP(t, map[string]string{"README.md": "invalid"}), "?overwrite=true"); rec.Code == http.StatusOK {
		t.Fatal("accepted invalid archive")
	}
	assertOld()
	registry.fail.Store(true)
	if rec := upload(valid, "?overwrite=true"); rec.Code == http.StatusOK {
		t.Fatal("accepted failed reload")
	}
	assertOld()
	if rec := upload(valid, "?overwrite=true"); rec.Code != http.StatusOK {
		t.Fatalf("overwrite: %d %s", rec.Code, rec.Body.String())
	}
	if got, err := os.ReadFile(filepath.Join(root, "skill.json")); err != nil || string(got) != `{"version":"2.0.0"}` {
		t.Fatalf("metadata: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "assets")); !os.IsNotExist(err) {
		t.Fatalf("old assets retained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "references", "nested", "new.md")); err != nil {
		t.Fatal(err)
	}
}

func TestAdminSkillDeleteWithWatcherRestoresAfterReloadFailure(t *testing.T) {
	f := newTestFixture(t)
	const key = "delete-rollback-skill"
	root := filepath.Join(f.cfg.Paths.SkillsCenterDir, key)
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{"SKILL.md": "# Delete rollback skill\n", "skill.json": `{"version":"1.0.0"}`, "nested/data": "old"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	registry := &failingOnceWatchedRegistry{Registry: f.registry}
	reloader := reload.NewRuntimeCatalogReloader(registry, f.modelRegistry, nil, nil, "", nil)
	f.server.deps.CatalogReloader = reloader
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload.StartBackgroundReloaders(ctx, f.cfg, reloader)
	registry.fail.Store(true)
	if _, err := f.server.deleteAdminSkill(ctx, key); err == nil {
		t.Fatal("expected reload failure")
	}
	for path, want := range map[string]string{"SKILL.md": "# Delete rollback skill\n", "skill.json": `{"version":"1.0.0"}`, "nested/data": "old"} {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(got) != want {
			t.Fatalf("lost %s: %q %v", path, got, err)
		}
	}
	if result, err := f.server.deleteAdminSkill(ctx, key); err != nil || result.Key != key || !result.Deleted {
		t.Fatalf("delete: %+v %v", result, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("directory remains: %v", err)
	}
}
