package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-platform/internal/api"
)

func TestAdminSkillTextSaveRollsBackWhenReloadFails(t *testing.T) {
	for _, endpoint := range []string{"/api/admin/source", "/api/admin/skills/file"} {
		t.Run(endpoint, func(t *testing.T) {
			fixture := newTestFixture(t)
			target := api.AdminSourceTarget{Type: "skill", Key: "mock-skill", Path: "SKILL.md"}
			before := getAdminSourceForTest(t, fixture.server, target)
			content := before.Content + "\nSaved change.\n"
			save := func(content, base string) *httptest.ResponseRecorder {
				t.Helper()
				var body any = api.UpdateAdminSourceRequest{Target: target, Content: content, BaseSHA256: base}
				if endpoint == "/api/admin/skills/file" {
					body = api.WriteAdminSkillFileRequest{Key: target.Key, Path: target.Path, Content: content, BaseSHA256: base}
				}
				payload, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				rec := httptest.NewRecorder()
				fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, endpoint, bytes.NewReader(payload)))
				return rec
			}

			fixture.server.deps.CatalogReloader = failingSkillImportReloader{}
			failed := save(content, before.SHA256)
			if failed.Code != http.StatusInternalServerError {
				t.Fatalf("failed reload status = %d: %s", failed.Code, failed.Body.String())
			}
			after := getAdminSourceForTest(t, fixture.server, target)
			if after.Content != before.Content || after.SHA256 != before.SHA256 {
				t.Fatal("failed save changed the file or invalidated the editor's base hash")
			}

			fixture.server.deps.CatalogReloader = fixture.catalogReloader
			if retry := save(content, before.SHA256); retry.Code != http.StatusOK {
				t.Fatalf("retry with original base failed: %d %s", retry.Code, retry.Body.String())
			}
			saved := getAdminSourceForTest(t, fixture.server, target)
			if saved.Content != content || saved.SHA256 == before.SHA256 {
				t.Fatal("successful retry did not publish the new file and hash")
			}
			if second := save(content+"Second change.\n", saved.SHA256); second.Code != http.StatusOK {
				t.Fatalf("consecutive save failed: %d %s", second.Code, second.Body.String())
			}
			if stale := save("stale overwrite", before.SHA256); stale.Code != http.StatusConflict {
				t.Fatalf("stale overwrite status = %d: %s", stale.Code, stale.Body.String())
			}
			if current := getAdminSourceForTest(t, fixture.server, target); current.Content != content+"Second change.\n" {
				t.Fatal("conflict changed the current file")
			}
		})
	}
}
