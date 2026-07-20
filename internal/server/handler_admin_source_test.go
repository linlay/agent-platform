package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agent-platform/internal/api"
)

func TestAdminSourceSkillTextReadWriteAndBinaryGuard(t *testing.T) {
	fixture := newTestFixture(t)
	target := api.AdminSourceTarget{Type: "skill", Key: "mock-skill", Path: "SKILL.md"}
	read := getAdminSourceForTest(t, fixture.server, target)
	if !strings.Contains(read.Content, "# Mock Skill") || read.Source.Path == "" || read.SHA256 == "" {
		t.Fatalf("unexpected skill source: %#v", read)
	}

	updated := "<!-- retained source comment -->\n" + read.Content
	saved := putAdminSourceForTest(t, fixture.server, target, updated, read.SHA256)
	if saved.Content != updated || saved.SHA256 == read.SHA256 {
		t.Fatalf("skill source was not updated as raw text: %#v", saved)
	}
	if again := getAdminSourceForTest(t, fixture.server, target); again.Content != updated {
		t.Fatalf("skill source did not persist raw content: %#v", again)
	}

	binary := httptest.NewRecorder()
	fixture.server.ServeHTTP(binary, httptest.NewRequest(http.MethodGet, "/api/admin/source?type=skill&key=mock-skill&path=assets%2Flogo.bin", nil))
	if binary.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("binary skill source status = %d body=%s", binary.Code, binary.Body.String())
	}
}

func TestAdminSourceRegistryReadWriteAndConflict(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	target := api.AdminSourceTarget{Type: "registry", Category: "providers", File: "mock.yml"}
	read := getAdminSourceForTest(t, fixture.server, target)
	updated := "# preserve registry comment\n" + read.Content
	saved := putAdminSourceForTest(t, fixture.server, target, updated, read.SHA256)
	if saved.Content != updated || saved.Source.Path == "" {
		t.Fatalf("registry source response = %#v", saved)
	}

	conflict := httptest.NewRecorder()
	payload, err := json.Marshal(api.UpdateAdminSourceRequest{Target: target, Content: read.Content, BaseSHA256: read.SHA256})
	if err != nil {
		t.Fatalf("marshal registry conflict request: %v", err)
	}
	fixture.server.ServeHTTP(conflict, httptest.NewRequest(http.MethodPut, "/api/admin/source", bytes.NewReader(payload)))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("registry stale source status = %d body=%s", conflict.Code, conflict.Body.String())
	}

	createdTarget := api.AdminSourceTarget{Type: "registry", Category: "providers", File: "created.yml"}
	createdContent := strings.Replace(updated, "key: mock", "key: created", 1)
	created := putAdminSourceForTest(t, fixture.server, createdTarget, createdContent, "")
	if created.Content != createdContent || created.SHA256 == "" {
		t.Fatalf("registry source creation response = %#v", created)
	}
	if reread := getAdminSourceForTest(t, fixture.server, createdTarget); reread.Content != createdContent {
		t.Fatalf("created registry source did not persist raw content: %#v", reread)
	}

	invalid := httptest.NewRecorder()
	invalidPayload, err := json.Marshal(api.UpdateAdminSourceRequest{Target: createdTarget, Content: "key: [unterminated", BaseSHA256: created.SHA256})
	if err != nil {
		t.Fatalf("marshal invalid registry source: %v", err)
	}
	fixture.server.ServeHTTP(invalid, httptest.NewRequest(http.MethodPut, "/api/admin/source", bytes.NewReader(invalidPayload)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid registry source status = %d body=%s", invalid.Code, invalid.Body.String())
	}
	if reread := getAdminSourceForTest(t, fixture.server, createdTarget); reread.Content != createdContent {
		t.Fatalf("invalid registry source changed file: %#v", reread)
	}
}

func TestAdminSourceAutomationReadWriteAndReload(t *testing.T) {
	fixture := newAutomationTestServer(t, false)
	created := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation/create", map[string]any{
		"name":        "Source Demo",
		"description": "Original description",
		"cron":        "0 9 * * *",
		"agentKey":    "demo-agent",
		"query":       map[string]any{"message": "hello"},
	})
	target := api.AdminSourceTarget{Type: "automation", Key: created.ID}
	read := getAdminSourceForTest(t, fixture.server, target)
	updated := "# retained source comment\n" + strings.Replace(read.Content, "name: Source Demo", "name: Source YAML Demo", 1)
	saved := putAdminSourceForTest(t, fixture.server, target, updated, read.SHA256)
	if saved.Content != updated {
		t.Fatalf("automation source was re-rendered instead of preserved: %#v", saved)
	}
	invalid := httptest.NewRecorder()
	invalidPayload, err := json.Marshal(api.UpdateAdminSourceRequest{Target: target, Content: "name: missing required fields\n", BaseSHA256: saved.SHA256})
	if err != nil {
		t.Fatalf("marshal invalid automation source: %v", err)
	}
	fixture.server.ServeHTTP(invalid, httptest.NewRequest(http.MethodPut, "/api/admin/source", bytes.NewReader(invalidPayload)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid automation source status = %d body=%s", invalid.Code, invalid.Body.String())
	}
	if reread := getAdminSourceForTest(t, fixture.server, target); reread.Content != updated {
		t.Fatalf("invalid automation source changed file: %#v", reread)
	}

	loaded := postAutomationJSON[api.AutomationDetailResponse](t, fixture.server, "/api/automation", map[string]any{"id": created.ID})
	if loaded.Name != "Source YAML Demo" {
		t.Fatalf("automation reload did not pick up source edit: %#v", loaded)
	}
}

func TestAdminSourceRejectsInvalidTargetAndOldAgentRoute(t *testing.T) {
	fixture := newTestFixture(t)
	invalid := httptest.NewRecorder()
	fixture.server.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/admin/source?type=skill&key=mock-skill&path=..%2Fagent.yml", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid source target status = %d body=%s", invalid.Code, invalid.Body.String())
	}

	legacy := httptest.NewRecorder()
	fixture.server.ServeHTTP(legacy, httptest.NewRequest(http.MethodGet, "/api/admin/agents/source?agentKey=mock-agent", nil))
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("legacy source route status = %d body=%s", legacy.Code, legacy.Body.String())
	}
}

func TestAdminSourceRejectsSymlinkedAgentYAML(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated permissions on Windows")
	}
	fixture := newTestFixture(t)
	path := filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
	backup := path + ".real"
	if err := os.Rename(path, backup); err != nil {
		t.Fatalf("move agent yaml: %v", err)
	}
	defer func() {
		_ = os.Remove(path)
		_ = os.Rename(backup, path)
	}()
	if err := os.Symlink(backup, path); err != nil {
		t.Fatalf("symlink agent yaml: %v", err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/source?type=agent&key=mock-agent", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("symlinked agent source status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func getAdminSourceForTest(t *testing.T, server *Server, target api.AdminSourceTarget) api.AdminSourceResponse {
	t.Helper()
	path := "/api/admin/source?type=" + target.Type
	if target.Key != "" {
		path += "&key=" + target.Key
	}
	if target.Path != "" {
		path += "&path=" + target.Path
	}
	if target.Category != "" {
		path += "&category=" + target.Category
	}
	if target.File != "" {
		path += "&file=" + target.File
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("read admin source status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AdminSourceResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode admin source response: %v", err)
	}
	return response.Data
}

func putAdminSourceForTest(t *testing.T, server *Server, target api.AdminSourceTarget, content string, baseSHA256 string) api.AdminSourceResponse {
	t.Helper()
	payload, err := json.Marshal(api.UpdateAdminSourceRequest{Target: target, Content: content, BaseSHA256: baseSHA256})
	if err != nil {
		t.Fatalf("marshal admin source update: %v", err)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/source", bytes.NewReader(payload)))
	if rec.Code != http.StatusOK {
		t.Fatalf("save admin source status = %d body=%s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AdminSourceResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode saved admin source response: %v", err)
	}
	return response.Data
}
