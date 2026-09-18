package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/reload"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAdminSkillTransactionCASAndFullSnapshot(t *testing.T) {
	f := newTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload.StartBackgroundReloaders(ctx, f.cfg, f.catalogReloader)
	call := func(req adminSkillTransactionRequest, status int) catalog.EditableSkillSnapshot {
		t.Helper()
		body, _ := json.Marshal(req)
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/skills/transaction", bytes.NewReader(body)))
		if rec.Code != status {
			t.Fatalf("%s status %d: %s", req.Operation, rec.Code, rec.Body.String())
		}
		var result api.ApiResponse[catalog.EditableSkillSnapshot]
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Data
	}
	archive := func(description string) []byte {
		return serverSkillImportZIP(t, map[string]string{"SKILL.md": "---\nname: cas-skill\ndescription: " + description + "\n---\n\nText.\n", "skill.json": "{}", ".runtime-env.json": "{}", "refs/data.txt": "data"})
	}
	missing := call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "snapshot"}, 200)
	if missing.Exists || missing.Revision != "missing" {
		t.Fatalf("missing snapshot: %#v", missing)
	}
	a := call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "replace", ExpectedRevision: missing.Revision, Archive: archive("A")}, 200)
	snapshot := call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "snapshot"}, 200)
	if snapshot.Revision != a.Revision || len(snapshot.Archive) == 0 {
		t.Fatalf("snapshot mismatch: %#v", snapshot)
	}
	zr, err := zip.NewReader(bytes.NewReader(snapshot.Archive), int64(len(snapshot.Archive)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, file := range zr.File {
		names[file.Name] = true
	}
	for _, name := range []string{"skill.json", ".runtime-env.json", "refs/data.txt"} {
		if !names[name] {
			t.Fatalf("snapshot omitted %s", name)
		}
	}
	b := call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "replace", ExpectedRevision: a.Revision, Archive: archive("B")}, 200)
	call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "replace", ExpectedRevision: a.Revision, Archive: snapshot.Archive}, 409)
	call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "delete", ExpectedRevision: a.Revision}, 409)
	call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "replace", Archive: archive("C")}, 400)
	deleted := call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "delete", ExpectedRevision: b.Revision}, 200)
	if deleted.Exists || deleted.Revision != "missing" {
		t.Fatalf("delete: %#v", deleted)
	}
	c := call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "replace", ExpectedRevision: "missing", Archive: archive("C")}, 200)
	call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "delete", ExpectedRevision: "missing"}, 409)
	call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "replace", ExpectedRevision: "missing", Archive: archive("A")}, 409)
	call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "delete", ExpectedRevision: b.Revision}, 409)
	// A complete prior ZIP can be restored only against the actual committed revision.
	restored := call(adminSkillTransactionRequest{Key: "cas-skill", Operation: "replace", ExpectedRevision: c.Revision, Archive: snapshot.Archive}, 200)
	if restored.Revision != a.Revision {
		t.Fatalf("round trip revision changed: %s != %s", restored.Revision, a.Revision)
	}
}

func TestAdminSkillTransactionRejectsTrailingJSON(t *testing.T) {
	f := newTestFixture(t)
	for _, body := range []string{
		`{"key":"demo","operation":"snapshot"} {"key":"other"}`,
		`{"key":"demo","operation":"replace","expectedRevision":"missing","archiveBase64":"!!!"}`,
	} {
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/skills/transaction", bytes.NewBufferString(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid body status %d: %s", rec.Code, rec.Body.String())
		}
	}
}

func TestAdminSkillTransactionReloadFailureRollsBack(t *testing.T) {
	f := newTestFixture(t)
	ctx := context.Background()
	archive := func(description string) []byte {
		return serverSkillImportZIP(t, map[string]string{"SKILL.md": "---\nname: cas-failure\ndescription: " + description + "\n---\n"})
	}
	first, err := f.server.transactAdminSkill(ctx, adminSkillTransactionRequest{Key: "cas-failure", Operation: "replace", ExpectedRevision: "missing", Archive: archive("old")})
	if err != nil {
		t.Fatal(err)
	}
	reg := &failingOnceWatchedRegistry{Registry: f.registry}
	reg.fail.Store(true)
	f.server.deps.CatalogReloader = reload.NewRuntimeCatalogReloader(reg, f.modelRegistry, nil, nil, "", nil)
	if _, err := f.server.transactAdminSkill(ctx, adminSkillTransactionRequest{Key: "cas-failure", Operation: "replace", ExpectedRevision: first.Revision, Archive: archive("new")}); err == nil {
		t.Fatal("expected reload error")
	}
	after, err := f.server.transactAdminSkill(ctx, adminSkillTransactionRequest{Key: "cas-failure", Operation: "snapshot"})
	if err != nil || after.Revision != first.Revision {
		t.Fatalf("rollback mismatch: %#v %v", after, err)
	}
	content, err := os.ReadFile(filepath.Join(f.cfg.Paths.SkillsCenterDir, "cas-failure", "SKILL.md"))
	if err != nil || !bytes.Contains(content, []byte("old")) {
		t.Fatalf("old content missing: %q %v", content, err)
	}
}
