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

func TestSkillPackageManifestNameOnlyAndConditionalEdit(t *testing.T) {
	f := newTestFixture(t)
	archive := serverSkillImportZIP(t, map[string]string{
		"package.json":  `{"name":"sample-suite"}`,
		"same/SKILL.md": "---\nname: same\ndescription: member\n---\nMember body.\n",
	})
	request := httptest.NewRequest(http.MethodPost, "/api/admin/skill-packages/import?key=sample-suite", bytes.NewReader(archive))
	request.Header.Set("Content-Type", "application/zip")
	response := httptest.NewRecorder()
	f.server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("minimal manifest import: %d %s", response.Code, response.Body.String())
	}
	packages := getAPIData[[]api.AdminSkillPackageResponse](t, f.server, "GET", "/api/admin/skill-packages", nil)
	if len(packages) != 1 || packages[0].DisplayName != "sample-suite" || packages[0].Version != "" || len(packages[0].Skills) != 1 || packages[0].Skills[0].ID != "sample-suite/same" {
		t.Fatalf("packages=%+v", packages)
	}
	file := getAPIData[skillPackageManifestResponse](t, f.server, "GET", "/api/admin/skill-packages/manifest?key=sample-suite", nil)
	body, _ := json.Marshal(map[string]string{"key": "sample-suite", "content": `{"name":"sample-suite","displayName":"测试技能包"}`, "baseSha256": file.SHA256})
	saved := getAPIData[skillPackageManifestResponse](t, f.server, "PUT", "/api/admin/skill-packages/manifest", body)
	if saved.SHA256 == file.SHA256 {
		t.Fatal("edit did not update revision")
	}
	packages = getAPIData[[]api.AdminSkillPackageResponse](t, f.server, "GET", "/api/admin/skill-packages", nil)
	if packages[0].DisplayName != "测试技能包" {
		t.Fatalf("displayName not updated: %+v", packages)
	}
	request = httptest.NewRequest(http.MethodPut, "/api/admin/skill-packages/manifest", bytes.NewReader(body))
	response = httptest.NewRecorder()
	f.server.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale edit expected 409: %d %s", response.Code, response.Body.String())
	}
	disk, err := os.ReadFile(filepath.Join(f.cfg.Paths.SkillsCenterDir, "sample-suite", "package.json"))
	if err != nil || bytes.Contains(disk, []byte(`"skills"`)) {
		t.Fatalf("member list must not be persisted: %s %v", disk, err)
	}
}

func TestSkillPackageManifestEditReloadFailureRestoresOriginal(t *testing.T) {
	f := newAgentSkillsTestFixture(t, false)
	writeProjectionPackage(t, f, "center-extra")
	original := getAPIData[skillPackageManifestResponse](t, f.server, "GET", "/api/admin/skill-packages/manifest?key=office", nil)
	f.server.deps.CatalogReloader = failingSkillImportReloader{}
	body, _ := json.Marshal(map[string]string{"key": "office", "content": `{"name":"office","displayName":"must rollback"}`, "baseSha256": original.SHA256})
	request := httptest.NewRequest(http.MethodPut, "/api/admin/skill-packages/manifest", bytes.NewReader(body))
	response := httptest.NewRecorder()
	f.server.ServeHTTP(response, request)
	if response.Code == http.StatusOK {
		t.Fatal("expected reload failure")
	}
	after := getAPIData[skillPackageManifestResponse](t, f.server, "GET", "/api/admin/skill-packages/manifest?key=office", nil)
	if after != original {
		t.Fatalf("manifest not rolled back: %+v", after)
	}
}

func TestSkillPackageAndStandaloneUpdatesAreIndependent(t *testing.T) {
	f := newTestFixture(t)
	standaloneDir := filepath.Join(f.cfg.Paths.SkillsCenterDir, "shared-name")
	writeTestSkill(t, f.cfg.Paths.SkillsCenterDir, "shared-name")
	standaloneBefore, err := os.ReadFile(filepath.Join(standaloneDir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	importPackage := func(version string) {
		t.Helper()
		content := "---\nname: shared-name\ndescription: Package " + version + "\n---\nPackage body " + version
		archive := serverSkillImportZIP(t, map[string]string{"package.json": `{"name":"independent-suite","version":"` + version + `"}`, "shared-name/SKILL.md": content})
		req := httptest.NewRequest(http.MethodPost, "/api/admin/skill-packages/import?key=independent-suite", bytes.NewReader(archive))
		req.Header.Set("Content-Type", "application/zip")
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("package install %s: %d %s", version, rec.Code, rec.Body.String())
		}
	}
	importPackage("1.0.0")
	importPackage("2.0.0")
	standaloneAfter, _ := os.ReadFile(filepath.Join(standaloneDir, "SKILL.md"))
	if !bytes.Equal(standaloneBefore, standaloneAfter) {
		t.Fatal("package update changed standalone skill")
	}
	memberPath := filepath.Join(f.cfg.Paths.SkillsCenterDir, "independent-suite", "shared-name", "SKILL.md")
	memberBefore, err := os.ReadFile(memberPath)
	if err != nil {
		t.Fatal(err)
	}
	archive := serverSkillImportZIP(t, map[string]string{"SKILL.md": "---\nname: shared-name\ndescription: Standalone update\n---\nNew standalone content"})
	body, contentType := skillImportBody(t, "shared-name", "shared-name.zip", archive)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import?overwrite=true", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("standalone update: %d %s", rec.Code, rec.Body.String())
	}
	memberAfter, _ := os.ReadFile(memberPath)
	if !bytes.Equal(memberBefore, memberAfter) {
		t.Fatal("standalone update changed package member")
	}
	standaloneAfter, _ = os.ReadFile(filepath.Join(standaloneDir, "SKILL.md"))
	if !bytes.Contains(standaloneAfter, []byte("New standalone content")) {
		t.Fatal("standalone was not updated")
	}
	getAPIData[api.DeleteAdminSkillPackageResponse](t, f.server, "POST", "/api/admin/skill-packages/delete", []byte(`{"key":"independent-suite"}`))
	remaining, _ := os.ReadFile(filepath.Join(standaloneDir, "SKILL.md"))
	if !bytes.Equal(standaloneAfter, remaining) {
		t.Fatal("package deletion changed standalone skill")
	}
}

func TestStandaloneSkillUninstallLeavesPackageMemberAndUpdateIndependent(t *testing.T) {
	f := newAgentSkillsTestFixture(t, false)
	writeProjectionPackage(t, f, "center-extra")
	memberPath := filepath.Join(f.cfg.Paths.SkillsCenterDir, "office", "center-extra", "SKILL.md")
	before, err := os.ReadFile(memberPath)
	if err != nil {
		t.Fatal(err)
	}
	deleted := getAPIData[api.DeleteAdminSkillResponse](t, f.server, http.MethodPost, "/api/admin/skills/delete", []byte(`{"key":"center-extra"}`))
	if !deleted.Deleted {
		t.Fatal("standalone was not deleted")
	}
	after, err := os.ReadFile(memberPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("standalone deletion changed package member: %v", err)
	}
	packages := getAPIData[[]api.AdminSkillPackageResponse](t, f.server, http.MethodGet, "/api/admin/skill-packages", nil)
	if len(packages) != 1 || len(packages[0].Skills) != 1 || packages[0].Skills[0].ID != "office/center-extra" {
		t.Fatalf("package changed: %+v", packages)
	}
	archive := serverSkillImportZIP(t, map[string]string{
		"package.json":          `{"name":"office","version":"2"}`,
		"center-extra/SKILL.md": "---\nname: center-extra\ndescription: Updated package member\n---\nUpdated package content",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/skill-packages/import?key=office", bytes.NewReader(archive))
	req.Header.Set("Content-Type", "application/zip")
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("package update: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Paths.SkillsCenterDir, "center-extra")); !os.IsNotExist(err) {
		t.Fatalf("package update recreated uninstalled standalone skill: %v", err)
	}
	after, err = os.ReadFile(memberPath)
	if err != nil || !bytes.Contains(after, []byte("Updated package content")) {
		t.Fatalf("package member not updated: %v", err)
	}
}
