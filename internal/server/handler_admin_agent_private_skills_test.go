package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

func TestAdminAgentPrivateSkillImportAndDelete(t *testing.T) {
	fixture := newTestFixture(t)
	editor, ok := fixture.registry.(interface {
		EditableAgent(key string) (catalog.EditableAgentFiles, bool, error)
		UpdateEditableAgent(key string, definition map[string]any, soulPrompt *string, agentsPrompt *string) (catalog.EditableAgentFiles, error)
	})
	if !ok {
		t.Fatal("fixture registry does not support Agent editing")
	}
	files, found, err := editor.EditableAgent("mock-agent")
	if err != nil || !found {
		t.Fatalf("load Agent before first private skill import: found=%v err=%v", found, err)
	}
	delete(files.Definition, "skillConfig")
	if _, err := editor.UpdateEditableAgent(files.Key, files.Definition, &files.SoulPrompt, &files.AgentsPrompt); err != nil {
		t.Fatalf("remove existing skillConfig: %v", err)
	}
	if err := fixture.registry.Reload(t.Context(), "agents"); err != nil {
		t.Fatalf("reload Agent without skillConfig: %v", err)
	}
	archive := serverSkillImportZIP(t, map[string]string{
		"SKILL.md":            "---\nname: personal-helper\ndescription: Agent only\n---\n\nUse it.\n",
		"references/guide.md": "guide\n",
	})
	body, contentType := agentPrivateSkillImportBody(t, "mock-agent", "", "personal-helper.zip", archive, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agents/skills/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d body=%s", rec.Code, rec.Body.String())
	}
	var imported api.ApiResponse[api.AdminAgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if !containsAdminPrivateSkill(imported.Data.PrivateSkills, "personal-helper") || !containsString(imported.Data.Skills, "personal-helper") {
		t.Fatalf("private skill is not listed and enabled: %#v", imported.Data)
	}
	privateDir := filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "skills", "personal-helper")
	if _, err := os.Stat(filepath.Join(privateDir, "references", "guide.md")); err != nil {
		t.Fatalf("private skill was not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "personal-helper")); !os.IsNotExist(err) {
		t.Fatalf("private import must not create a center skill: %v", err)
	}

	deleteBody, err := json.Marshal(api.DeleteAdminAgentPrivateSkillRequest{AgentKey: "mock-agent", Key: "personal-helper"})
	if err != nil {
		t.Fatalf("marshal delete: %v", err)
	}
	deleted := httptest.NewRecorder()
	fixture.server.ServeHTTP(deleted, httptest.NewRequest(http.MethodPost, "/api/admin/agents/skills/delete", bytes.NewReader(deleteBody)))
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(privateDir); !os.IsNotExist(err) {
		t.Fatalf("private skill directory remains after delete: %v", err)
	}
	var deletedDetail api.ApiResponse[api.AdminAgentDetailResponse]
	if err := json.Unmarshal(deleted.Body.Bytes(), &deletedDetail); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if containsAdminPrivateSkill(deletedDetail.Data.PrivateSkills, "personal-helper") || containsString(deletedDetail.Data.Skills, "personal-helper") {
		t.Fatalf("deleted private skill remains in detail: %#v", deletedDetail.Data)
	}
}

func TestAdminAgentPrivateSkillOverrideRequiresConfirmationAndDoesNotBlockCenterDelete(t *testing.T) {
	fixture := newTestFixture(t)
	archive := serverSkillImportZIP(t, map[string]string{"SKILL.md": "# Private Mock Skill\n"})
	body, contentType := agentPrivateSkillImportBody(t, "mock-agent", "mock-skill", "mock-skill.zip", archive, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agents/skills/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "requiresConfirmation") {
		t.Fatalf("override without confirmation = %d body=%s", rec.Code, rec.Body.String())
	}

	body, contentType = agentPrivateSkillImportBodyWithConfirmationField(t, "mock-agent", "mock-skill", "mock-skill.zip", archive, "confirmMarketOverride")
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/admin/agents/skills/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "requiresConfirmation") {
		t.Fatalf("removed confirmation field must be ignored = %d body=%s", rec.Code, rec.Body.String())
	}

	body, contentType = agentPrivateSkillImportBody(t, "mock-agent", "mock-skill", "mock-skill.zip", archive, true)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/admin/agents/skills/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmed override = %d body=%s", rec.Code, rec.Body.String())
	}
	definition, found := fixture.registry.AgentDefinition("mock-agent")
	if !found {
		t.Fatal("reloaded agent definition is missing")
	}
	runtimeSkill, err := os.ReadFile(filepath.Join(definition.RuntimeDir, "skills", "mock-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("read assembled private override: %v", err)
	}
	if !strings.Contains(string(runtimeSkill), "Private Mock Skill") {
		t.Fatalf("runtime should prefer Agent-private skill, got:\n%s", runtimeSkill)
	}

	deleteCenterBody := mustSkillJSON(t, api.DeleteAdminSkillRequest{Key: "mock-skill"})
	centerDelete := httptest.NewRecorder()
	fixture.server.ServeHTTP(centerDelete, httptest.NewRequest(http.MethodPost, "/api/admin/skills/delete", bytes.NewReader(deleteCenterBody)))
	if centerDelete.Code != http.StatusOK {
		t.Fatalf("center delete should not be blocked by private override: %d body=%s", centerDelete.Code, centerDelete.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "skills", "mock-skill", "SKILL.md")); err != nil {
		t.Fatalf("private override was altered by center delete: %v", err)
	}
}

func TestAdminAgentPrivateSkillImportRollsBackOnReloadFailure(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.catalogReloader = failingSkillImportReloader{}
	fixture.server = newServerFromFixture(t, fixture)
	archive := serverSkillImportZIP(t, map[string]string{"SKILL.md": "# Rollback\n"})
	body, contentType := agentPrivateSkillImportBody(t, "mock-agent", "rollback-skill", "rollback-skill.zip", archive, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agents/skills/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("reload failure = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "skills", "rollback-skill")); !os.IsNotExist(err) {
		t.Fatalf("rollback directory remains: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "agent.yml"))
	if err != nil {
		t.Fatalf("read restored agent config: %v", err)
	}
	if strings.Contains(string(content), "rollback-skill") {
		t.Fatalf("rollback skill leaked into agent config:\n%s", content)
	}
}

func TestAdminAgentPrivateSkillDeleteRollsBackOnReloadFailure(t *testing.T) {
	fixture := newTestFixture(t)
	archive := serverSkillImportZIP(t, map[string]string{"SKILL.md": "# Rollback delete\n"})
	body, contentType := agentPrivateSkillImportBody(t, "mock-agent", "rollback-delete", "rollback-delete.zip", archive, false)
	imported := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agents/skills/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(imported, req)
	if imported.Code != http.StatusOK {
		t.Fatalf("seed import = %d body=%s", imported.Code, imported.Body.String())
	}

	fixture.catalogReloader = failingSkillImportReloader{}
	fixture.server = newServerFromFixture(t, fixture)
	deleteBody := mustSkillJSON(t, api.DeleteAdminAgentPrivateSkillRequest{AgentKey: "mock-agent", Key: "rollback-delete"})
	deleted := httptest.NewRecorder()
	fixture.server.ServeHTTP(deleted, httptest.NewRequest(http.MethodPost, "/api/admin/agents/skills/delete", bytes.NewReader(deleteBody)))
	if deleted.Code != http.StatusInternalServerError {
		t.Fatalf("delete reload failure = %d body=%s", deleted.Code, deleted.Body.String())
	}
	privateDir := filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "skills", "rollback-delete")
	if _, err := os.Stat(filepath.Join(privateDir, "SKILL.md")); err != nil {
		t.Fatalf("delete rollback did not restore private skill: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "agent.yml"))
	if err != nil {
		t.Fatalf("read restored agent config: %v", err)
	}
	if !strings.Contains(string(content), "rollback-delete") {
		t.Fatalf("delete rollback removed skill from agent config:\n%s", content)
	}
}

func TestAdminAgentPrivateSkillImportRejectsInvalidArchiveAndFlatAgent(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}, testFixtureOptions{
		setupRuntime: func(_ string, cfg *config.Config) {
			flat := strings.Join([]string{
				"key: flat-agent",
				"name: Flat Agent",
				"modelConfig:",
				"  modelKey: mock-model",
				"runtimeConfig:",
				"  workspaceRoot: " + filepath.ToSlash(filepath.Join(filepath.Dir(cfg.Paths.AgentsDir), "workspace")),
				"mode: REACT",
			}, "\n")
			if err := os.WriteFile(filepath.Join(cfg.Paths.AgentsDir, "flat-agent.yml"), []byte(flat), 0o644); err != nil {
				t.Fatalf("write flat agent: %v", err)
			}
		},
	})

	invalidArchive := serverSkillImportZIP(t, map[string]string{"README.md": "missing skill manifest"})
	body, contentType := agentPrivateSkillImportBody(t, "mock-agent", "invalid-private", "invalid-private.zip", invalidArchive, false)
	invalid := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agents/skills/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(invalid, req)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid archive = %d body=%s", invalid.Code, invalid.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "skills", "invalid-private")); !os.IsNotExist(err) {
		t.Fatalf("invalid archive left a private skill directory: %v", err)
	}

	validArchive := serverSkillImportZIP(t, map[string]string{"SKILL.md": "# Flat agent skill\n"})
	body, contentType = agentPrivateSkillImportBody(t, "flat-agent", "flat-private", "flat-private.zip", validArchive, false)
	flat := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/admin/agents/skills/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(flat, req)
	if flat.Code != http.StatusConflict || !strings.Contains(flat.Body.String(), "directory agent") {
		t.Fatalf("flat agent import = %d body=%s", flat.Code, flat.Body.String())
	}
}

func TestAdminAgentPrivateSkillDetailUsesRelativeDiagnosticPaths(t *testing.T) {
	fixture := newTestFixture(t)
	privateDir := filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "skills", "broken-private")
	if err := os.MkdirAll(privateDir, 0o755); err != nil {
		t.Fatalf("mkdir invalid private skill: %v", err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/agents/detail?agentKey=mock-agent", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin detail status = %d body=%s", rec.Code, rec.Body.String())
	}
	var detail api.ApiResponse[api.AdminAgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode admin detail: %v", err)
	}
	for _, skill := range detail.Data.PrivateSkills {
		if skill.Key != "broken-private" {
			continue
		}
		if len(skill.Diagnostics) == 0 {
			t.Fatal("invalid private skill diagnostics are missing")
		}
		if got := skill.Diagnostics[0].SourcePath; got != "SKILL.md" || filepath.IsAbs(got) {
			t.Fatalf("private diagnostic path = %q, want relative SKILL.md", got)
		}
		return
	}
	t.Fatal("invalid private skill is missing from admin detail")
}

func agentPrivateSkillImportBody(t *testing.T, agentKey, key, filename string, data []byte, confirmOverride bool) (io.Reader, string) {
	confirmationField := ""
	if confirmOverride {
		confirmationField = "confirmCenterOverride"
	}
	return agentPrivateSkillImportBodyWithConfirmationField(t, agentKey, key, filename, data, confirmationField)
}

func agentPrivateSkillImportBodyWithConfirmationField(t *testing.T, agentKey, key, filename string, data []byte, confirmationField string) (io.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{"agentKey": agentKey, "key": key} {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if confirmationField != "" {
		if err := writer.WriteField(confirmationField, "true"); err != nil {
			t.Fatalf("write confirmation: %v", err)
		}
	}
	file, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	return &body, writer.FormDataContentType()
}

func containsAdminPrivateSkill(items []api.AdminAgentPrivateSkill, key string) bool {
	for _, item := range items {
		if item.Key == key {
			return true
		}
	}
	return false
}
