package server

import (
	"agent-platform/internal/catalog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminConnectorSkillListAndDetail(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	root := fixture.server.deps.Config.Paths.EffectiveConnectorsCenterDir()
	writeMCPConnectorForTest(t, root, "demo")
	dir := filepath.Join(root, "demo", "skills", "guide")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: guide\ndescription: >\n  A multi-line\n  connector guide.\nmetadata:\n  version: 2.1.0\ntriggers:\n  - query\n  - report\n---\n# Guide\nComplete instructions.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	list := getAPIData[struct {
		ConnectorID string                          `json:"connectorId"`
		Skills      []catalog.ConnectorSkillSummary `json:"skills"`
	}](t, fixture.server, http.MethodGet, "/api/admin/connectors/skills?id=demo", nil)
	if list.ConnectorID != "demo" || len(list.Skills) != 1 {
		t.Fatalf("list=%+v", list)
	}
	skill := list.Skills[0]
	if skill.Name != "guide" || skill.Description != "A multi-line connector guide." || skill.Version != "2.1.0" || len(skill.Triggers) != 2 {
		t.Fatalf("metadata=%+v", skill)
	}
	detail := getAPIData[catalog.ConnectorSkillDetail](t, fixture.server, http.MethodGet, "/api/admin/connectors/skills/detail?id=demo&name=guide", nil)
	if detail.Content != content || detail.Skill.Path != "skills/guide/SKILL.md" || len(detail.SHA256) != 64 {
		t.Fatalf("detail=%+v", detail)
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/api/admin/connectors/skills?id=missing", 404},
		{http.MethodGet, "/api/admin/connectors/skills/detail?id=demo&name=missing", 404},
		{http.MethodGet, "/api/admin/connectors/skills/detail?id=demo&name=../guide", 400},
		{http.MethodGet, "/api/admin/connectors/skills/detail?id=demo", 400},
		{http.MethodGet, "/api/admin/connectors/skills?id=../demo", 400},
		{http.MethodPost, "/api/admin/connectors/skills?id=demo", 405},
		{http.MethodPut, "/api/admin/connectors/skills/detail?id=demo&name=guide", 405},
		{http.MethodGet, "/api/connectors/skills?id=demo", 404},
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.status {
			t.Fatalf("%s %s = %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), root) {
			t.Fatal("local path leaked")
		}
	}
}
