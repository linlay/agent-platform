package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
)

func TestAdminSkillPackageImportAndDelete(t *testing.T) {
	fixture := newTestFixture(t)
	archive := serverSkillImportZIP(t, map[string]string{
		"manifest.json":                `{"schemaVersion":1,"type":"skill-package","id":"office-pack","version":"1.0.0","skills":[{"id":"word-helper","version":"1.0.0","path":"skills/word-helper/"},{"id":"excel-helper","version":"2.0.0","path":"skills/excel-helper/"}]}`,
		"skills/word-helper/SKILL.md":  "---\nname: word-helper\ndescription: Word helper\nmetadata:\n  version: 1.0.0\n---\n\nUse Word.\n",
		"skills/excel-helper/SKILL.md": "---\nname: excel-helper\ndescription: Excel helper\nmetadata:\n  version: 2.0.0\n---\n\nUse Excel.\n",
	})

	request := httptest.NewRequest(http.MethodPost, "/api/admin/skill-packages/import?key=office-pack&version=1.0.0", bytes.NewReader(archive))
	request.Header.Set("Content-Type", "application/zip")
	recorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("package import expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var imported api.ApiResponse[api.AdminSkillPackageResponse]
	if err := json.Unmarshal(recorder.Body.Bytes(), &imported); err != nil {
		t.Fatalf("decode package import: %v", err)
	}
	if imported.Data.ID != "office-pack" || len(imported.Data.Skills) != 2 {
		t.Fatalf("unexpected package response: %#v", imported.Data)
	}
	for _, id := range []string{"word-helper", "excel-helper"} {
		if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, id, "SKILL.md")); err != nil {
			t.Fatalf("missing installed child %s: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, ".package", "office-pack.json")); err != nil {
		t.Fatalf("missing package state: %v", err)
	}
	packages := getAPIData[[]api.AdminSkillPackageResponse](t, fixture.server, http.MethodGet, "/api/admin/skill-packages", nil)
	if len(packages) != 1 || packages[0].ID != "office-pack" || len(packages[0].Skills) != 2 {
		t.Fatalf("unexpected package list: %#v", packages)
	}
	childDeleteBody, _ := json.Marshal(api.DeleteAdminSkillPackageSkillRequest{PackageID: "office-pack", SkillID: "word-helper"})
	childDeleted := getAPIData[api.DeleteAdminSkillPackageSkillResponse](t, fixture.server, http.MethodPost, "/api/admin/skill-packages/skills/delete", childDeleteBody)
	if !childDeleted.Deleted || childDeleted.PackageDeleted || len(childDeleted.RemainingSkills) != 1 {
		t.Fatalf("unexpected child delete response: %#v", childDeleted)
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "word-helper")); !os.IsNotExist(err) {
		t.Fatalf("deleted package child remains: %v", err)
	}

	deleteBody, _ := json.Marshal(api.DeleteAdminSkillPackageRequest{Key: "office-pack"})
	deleted := getAPIData[api.DeleteAdminSkillPackageResponse](t, fixture.server, http.MethodPost, "/api/admin/skill-packages/delete", deleteBody)
	if !deleted.Deleted || len(deleted.Skills) != 1 {
		t.Fatalf("unexpected delete response: %#v", deleted)
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, ".package", "office-pack.json")); !os.IsNotExist(err) {
		t.Fatalf("package state remains after delete: %v", err)
	}
}

func TestAdminSkillPackageImportRollsBackWhenCatalogReloadFails(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.server.deps.CatalogReloader = failingSkillImportReloader{}
	archive := serverSkillImportZIP(t, map[string]string{
		"manifest.json":               `{"schemaVersion":1,"type":"skill-package","id":"office-pack","version":"1.0.0","skills":[{"id":"word-helper","version":"1.0.0","path":"skills/word-helper/"}]}`,
		"skills/word-helper/SKILL.md": "---\nname: word-helper\ndescription: Word helper\n---\n\nUse Word.\n",
	})
	request := httptest.NewRequest(http.MethodPost, "/api/admin/skill-packages/import?key=office-pack&version=1.0.0", bytes.NewReader(archive))
	request.Header.Set("Content-Type", "application/zip")
	recorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusOK {
		t.Fatalf("expected reload failure, got %s", recorder.Body.String())
	}
	for _, target := range []string{
		filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "word-helper"),
		filepath.Join(fixture.cfg.Paths.SkillsCenterDir, ".package", "office-pack.json"),
	} {
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("rollback left %s: %v", target, err)
		}
	}
}
