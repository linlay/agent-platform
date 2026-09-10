package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func autoImportSkillZIP(t *testing.T, server http.Handler, key string, files map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := skillImportBody(t, key, "download.zip", serverSkillImportZIP(t, files))
	request := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import", body)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func TestAdminSkillAutoImportDetectsSingleSkill(t *testing.T) {
	for _, prefix := range []string{"", "wrapper/"} {
		t.Run(prefix, func(t *testing.T) {
			fixture := newTestFixture(t)
			response := autoImportSkillZIP(t, fixture.server, "", map[string]string{
				prefix + "SKILL.md":        "---\nkey: detected-skill\nname: Display Name\ndescription: Test\n---\n\nUse it.\n",
				prefix + "assets/info.txt": "asset content",
			})
			if response.Code != http.StatusOK {
				t.Fatalf("import expected 200, got %d: %s", response.Code, response.Body.String())
			}
			var result api.ApiResponse[api.AdminSkillImportResponse]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Data.Kind != "skill" || result.Data.AdminSkillDetailResponse == nil || result.Data.Skill.Key != "detected-skill" || result.Data.Package != nil {
				t.Fatalf("wrong detected skill: %#v", result.Data)
			}
			if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "detected-skill", "assets", "info.txt")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdminSkillAutoImportDetectsPackageAndUpdatesMembers(t *testing.T) {
	fixture := newTestFixture(t)
	ids := []string{"shared", "todo", "meeting", "media", "calendar", "contact", "disk", "doc-manage", "message", "email", "sheet", "doc", "smartsheet", "smartpage"}
	files := map[string]string{}
	members := make([]map[string]any, 0, len(ids))
	for index, suffix := range ids {
		id := "wecomcli-" + suffix
		members = append(members, map[string]any{
			"id": id, "name": suffix, "version": "1.1.1", "path": "skills/" + id + "/",
			"optional": false, "platform": "universal", "sortOrder": index + 1, "sha256": strings.Repeat("a", 64),
		})
		files["skills/"+id+"/SKILL.md"] = fmt.Sprintf("---\nname: %s\ndescription: Test\nversion: 1.1.1\n---\n\nUse it.\n", id)
	}
	manifest := map[string]any{"schemaVersion": 1, "type": "skill-package", "id": "wecomcli-suite", "name": "企业微信办公全家桶", "version": "1.1.0", "generatedAt": "2026-09-09T08:48:53Z", "skills": members}
	encoded, _ := json.Marshal(manifest)
	files["manifest.json"] = string(encoded)
	// A legacy optional key must never rename a package or its members.
	response := autoImportSkillZIP(t, fixture.server, "download", files)
	if response.Code != http.StatusOK {
		t.Fatalf("package expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var result api.ApiResponse[api.AdminSkillImportResponse]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Kind != "skill-package" || result.Data.Package == nil || result.Data.Package.ID != "wecomcli-suite" || result.Data.Package.Name != "企业微信办公全家桶" || len(result.Data.Package.Skills) != 14 || result.Data.AdminSkillDetailResponse != nil {
		t.Fatalf("unexpected package result: %s", response.Body.String())
	}
	for _, suffix := range ids {
		if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "wecomcli-"+suffix, "SKILL.md")); err != nil {
			t.Fatal(err)
		}
	}
	manifest["version"] = "1.2.0"
	manifest["skills"] = members[:1]
	encoded, _ = json.Marshal(manifest)
	updatedFiles := map[string]string{"manifest.json": string(encoded), "skills/wecomcli-shared/SKILL.md": files["skills/wecomcli-shared/SKILL.md"]}
	response = autoImportSkillZIP(t, fixture.server, "", updatedFiles)
	if response.Code != http.StatusOK {
		t.Fatalf("update expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "wecomcli-todo")); !os.IsNotExist(err) {
		t.Fatalf("removed member remains: %v", err)
	}
	packages := getAPIData[[]api.AdminSkillPackageResponse](t, fixture.server, http.MethodGet, "/api/admin/skill-packages", nil)
	if len(packages) != 1 || packages[0].Version != "1.2.0" || packages[0].Name != "企业微信办公全家桶" || len(packages[0].Skills) != 1 {
		t.Fatalf("unexpected updated package state: %#v", packages)
	}
}

func TestAdminSkillAutoImportRejectsBrokenPackageWithoutFallingBack(t *testing.T) {
	for _, manifest := range []string{
		`{"schemaVersion":1,"type":"skill-package","id":"broken","version":"1","skills":[{"id":"missing","version":"1","path":"skills/missing/"}]}`,
		`{"schemaVersion":2,"type":"skill-package","id":"broken","version":"1","skills":[]}`,
		`{"type":"skill-package",`,
	} {
		t.Run(manifest, func(t *testing.T) {
			fixture := newTestFixture(t)
			response := autoImportSkillZIP(t, fixture.server, "fallback", map[string]string{
				"manifest.json": manifest, "SKILL.md": "---\nname: fallback\ndescription: Test\n---\nUse it.",
			})
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("expected 422, got %d: %s", response.Code, response.Body.String())
			}
			for _, key := range []string{"fallback", "missing", "broken"} {
				if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, key)); !os.IsNotExist(err) {
					t.Fatalf("failed import left %s: %v", key, err)
				}
			}
		})
	}
}

func TestAdminSkillAutoImportPackageReloadFailureRollsBack(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.server.deps.CatalogReloader = failingSkillImportReloader{}
	response := autoImportSkillZIP(t, fixture.server, "", map[string]string{
		"manifest.json":               `{"schemaVersion":1,"type":"skill-package","id":"office-pack","version":"1","skills":[{"id":"word-helper","version":"1","path":"skills/word-helper/"}]}`,
		"skills/word-helper/SKILL.md": "---\nname: word-helper\ndescription: Test\n---\nUse it.",
	})
	if response.Code == http.StatusOK {
		t.Fatal("expected reload failure")
	}
	for _, target := range []string{"word-helper", ".package/office-pack.json"} {
		if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, target)); !os.IsNotExist(err) {
			t.Fatalf("rollback left %s: %v", target, err)
		}
	}
}
