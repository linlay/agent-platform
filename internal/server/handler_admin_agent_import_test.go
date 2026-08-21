package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
)

func TestAdminAgentImportCreatesCompleteAgentAndKeepsInvalidAgent(t *testing.T) {
	fixture := newTestFixture(t)
	readyArchive := serverSkillImportZIP(t, map[string]string{
		"portable/agent.yml":       "key: portable-agent\nname: Portable Agent\nmode: REACT\nmodelConfig:\n  modelKey: mock-model\n",
		"portable/SOUL.md":         "Portable soul\n",
		"portable/assets/card.txt": "card\n",
	})
	body, contentType := agentImportBody(t, "portable.zip", readyArchive, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agents/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready import returned %d: %s", rec.Code, rec.Body.String())
	}
	ready := decodeAgentImportResponse(t, rec)
	if ready.Key != "portable-agent" || ready.Name != "Portable Agent" || ready.Status != "ready" {
		t.Fatalf("unexpected ready import: %#v", ready)
	}
	if ready.Source == nil || ready.Source.Kind != "directory" || !strings.HasSuffix(ready.Source.Path, filepath.Join("portable-agent", "agent.yml")) {
		t.Fatalf("unexpected ready source: %#v", ready.Source)
	}
	if content, err := os.ReadFile(filepath.Join(fixture.cfg.Paths.AgentsDir, "portable-agent", "assets", "card.txt")); err != nil || string(content) != "card\n" {
		t.Fatalf("complete agent resource was not imported: content=%q err=%v", content, err)
	}

	invalidArchive := serverSkillImportZIP(t, map[string]string{
		"agent.yml": "key: invalid-import\nname: Invalid Import\nmode: CODER\nmodelConfig:\n  modelKey: mock-model\nruntimeConfig:\n  workspaceRoot: /definitely/missing/agent-import-workspace\n",
	})
	body, contentType = agentImportBody(t, "invalid.zip", invalidArchive, false)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/admin/agents/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("invalid agent import returned %d: %s", rec.Code, rec.Body.String())
	}
	invalid := decodeAgentImportResponse(t, rec)
	if invalid.Key != "invalid-import" || invalid.Status != "invalid" || len(invalid.Diagnostics) == 0 {
		t.Fatalf("expected retained invalid agent diagnostics, got %#v", invalid)
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.AgentsDir, "invalid-import", "agent.yml")); err != nil {
		t.Fatalf("invalid imported agent should remain editable: %v", err)
	}
}

func TestAdminAgentImportRequiresExplicitOverwrite(t *testing.T) {
	fixture := newTestFixture(t)
	existingSoulPath := filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "SOUL.md")
	if err := os.WriteFile(existingSoulPath, []byte("old soul\n"), 0o644); err != nil {
		t.Fatalf("write old soul: %v", err)
	}
	archive := serverSkillImportZIP(t, map[string]string{
		"agent.yml":        "key: mock-agent\nname: Imported Mock\nmode: REACT\nmodelConfig:\n  modelKey: mock-model\n",
		"new-resource.txt": "replacement\n",
	})

	body, contentType := agentImportBody(t, "mock-agent.zip", archive, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agents/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected overwrite conflict, got %d: %s", rec.Code, rec.Body.String())
	}
	var conflict api.ApiResponse[map[string]any]
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	errorData, _ := conflict.Data["error"].(map[string]any)
	if errorData["code"] != "agent_exists" || errorData["agentKey"] != "mock-agent" || errorData["overwriteRequired"] != true {
		t.Fatalf("unexpected conflict payload: %#v", conflict.Data)
	}
	if _, err := os.Stat(existingSoulPath); err != nil {
		t.Fatalf("existing agent changed before confirmation: %v", err)
	}

	body, contentType = agentImportBody(t, "mock-agent.zip", archive, true)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/admin/agents/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirmed overwrite returned %d: %s", rec.Code, rec.Body.String())
	}
	replaced := decodeAgentImportResponse(t, rec)
	if replaced.Key != "mock-agent" || replaced.Name != "Imported Mock" {
		t.Fatalf("unexpected overwrite response: %#v", replaced)
	}
	if _, err := os.Stat(existingSoulPath); !os.IsNotExist(err) {
		t.Fatalf("old source should be fully replaced, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "new-resource.txt")); err != nil {
		t.Fatalf("replacement resource missing: %v", err)
	}
}

func TestAdminAgentImportRollsBackOnHardReloadFailure(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.server.deps.CatalogReloader = failingSkillImportReloader{}
	archive := serverSkillImportZIP(t, map[string]string{
		"agent.yml": "key: rollback-import\nname: Rollback Import\nmode: REACT\nmodelConfig:\n  modelKey: mock-model\n",
	})
	body, contentType := agentImportBody(t, "rollback.zip", archive, false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agents/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected reload failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.AgentsDir, "rollback-import")); !os.IsNotExist(err) {
		t.Fatalf("failed import should be removed, got %v", err)
	}
}

func TestAdminAgentImportRestoresExistingAgentOnHardReloadFailure(t *testing.T) {
	fixture := newTestFixture(t)
	existingConfigPath := filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "agent.yml")
	original, err := os.ReadFile(existingConfigPath)
	if err != nil {
		t.Fatalf("read original Agent: %v", err)
	}
	fixture.server.deps.CatalogReloader = failingSkillImportReloader{}
	archive := serverSkillImportZIP(t, map[string]string{
		"agent.yml":        "key: mock-agent\nname: Broken Replacement\nmode: REACT\nmodelConfig:\n  modelKey: mock-model\n",
		"replacement-only": "new",
	})
	body, contentType := agentImportBody(t, "replacement.zip", archive, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/agents/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected reload failure, got %d: %s", rec.Code, rec.Body.String())
	}
	restored, err := os.ReadFile(existingConfigPath)
	if err != nil {
		t.Fatalf("read restored Agent: %v", err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("existing Agent was not restored after hard reload failure")
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.AgentsDir, "mock-agent", "replacement-only")); !os.IsNotExist(err) {
		t.Fatalf("replacement resource survived rollback: %v", err)
	}
}

func TestAdminAgentImportReportsRollbackFailureDetails(t *testing.T) {
	fixture := newTestFixture(t)
	editor := &failingAgentArchiveRollbackRegistry{rollbackErr: errors.New("restore failed")}
	err := fixture.server.rollbackAgentArchiveImport(
		context.Background(),
		editor,
		&catalog.EditableAgentArchiveMutation{Key: "rollback-agent"},
		errors.New("catalog reload failed"),
	)
	var statusErr agentStatusError
	if !errors.As(err, &statusErr) || statusErr.status != http.StatusInternalServerError || statusErr.code != "rollback_failed" {
		t.Fatalf("unexpected rollback error: %T %v", err, err)
	}
	if statusErr.data["agentKey"] != "rollback-agent" || statusErr.data["rollbackError"] != "restore failed" {
		t.Fatalf("rollback diagnostics missing: %#v", statusErr.data)
	}
}

func TestAdminAgentImportMapsArchiveStatusCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "upload size", err: catalog.ErrAgentArchiveUploadTooLarge, status: http.StatusRequestEntityTooLarge},
		{name: "expanded size", err: catalog.ErrAgentArchiveTooLarge, status: http.StatusRequestEntityTooLarge},
		{name: "entry count", err: catalog.ErrAgentArchiveTooManyFiles, status: http.StatusRequestEntityTooLarge},
		{name: "non zip", err: catalog.ErrAgentArchiveInvalid, status: http.StatusUnsupportedMediaType},
		{name: "validation", err: &catalog.AgentArchiveValidationError{Diagnostics: []catalog.AgentArchiveDiagnostic{{Code: "invalid_layout", Message: "invalid layout", SourcePath: "agent.yml"}}}, status: http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapped := mapAgentArchiveEditError(tc.err)
			var statusErr agentStatusError
			if !errors.As(mapped, &statusErr) || statusErr.status != tc.status {
				t.Fatalf("mapped error = %T %#v, want status %d", mapped, mapped, tc.status)
			}
		})
	}
}

func TestAdminAgentImportMapsArchiveErrors(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		name       string
		filename   string
		archive    []byte
		overwrite  string
		wantStatus int
		wantText   string
	}{
		{name: "wrong extension", filename: "agent.txt", archive: []byte("not zip"), wantStatus: http.StatusUnsupportedMediaType},
		{name: "invalid zip", filename: "agent.zip", archive: []byte("not zip"), wantStatus: http.StatusUnsupportedMediaType},
		{name: "invalid layout", filename: "agent.zip", archive: serverSkillImportZIP(t, map[string]string{"README.md": "missing"}), wantStatus: http.StatusUnprocessableEntity, wantText: "missing_agent_config"},
		{name: "invalid overwrite", filename: "agent.zip", archive: serverSkillImportZIP(t, map[string]string{"agent.yml": "key: okay\nname: Okay\nmode: REACT\nmodelConfig:\n  modelKey: mock-model\n"}), overwrite: "sometimes", wantStatus: http.StatusBadRequest, wantText: "overwrite must be a boolean"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, contentType := agentImportBodyWithOverwrite(t, tc.filename, tc.archive, tc.overwrite)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/admin/agents/import", body)
			req.Header.Set("Content-Type", contentType)
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus || (tc.wantText != "" && !strings.Contains(rec.Body.String(), tc.wantText)) {
				t.Fatalf("got %d: %s, want %d containing %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantText)
			}
		})
	}
}

func agentImportBody(t *testing.T, filename string, data []byte, overwrite bool) (io.Reader, string) {
	value := ""
	if overwrite {
		value = "true"
	}
	return agentImportBodyWithOverwrite(t, filename, data, value)
}

func agentImportBodyWithOverwrite(t *testing.T, filename string, data []byte, overwrite string) (io.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if overwrite != "" {
		if err := writer.WriteField("overwrite", overwrite); err != nil {
			t.Fatalf("write overwrite: %v", err)
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

func decodeAgentImportResponse(t *testing.T, rec *httptest.ResponseRecorder) api.AdminAgentDetailResponse {
	t.Helper()
	var response api.ApiResponse[api.AdminAgentDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode agent import response: %v", err)
	}
	return response.Data
}

type failingAgentArchiveRollbackRegistry struct {
	rollbackErr error
}

func (r *failingAgentArchiveRollbackRegistry) BeginImportEditableAgentArchive(io.ReaderAt, int64, bool) (*catalog.EditableAgentArchiveMutation, error) {
	return nil, errors.New("not implemented")
}

func (r *failingAgentArchiveRollbackRegistry) RollbackEditableAgentArchiveMutation(*catalog.EditableAgentArchiveMutation) error {
	return r.rollbackErr
}

func (r *failingAgentArchiveRollbackRegistry) CommitEditableAgentArchiveMutation(*catalog.EditableAgentArchiveMutation) error {
	return nil
}
