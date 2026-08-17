package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

func TestAdminSkillsManifestLazyContentAndMutations(t *testing.T) {
	fixture := newTestFixture(t)
	items := getAPIData[[]api.AdminSkillSummary](t, fixture.server, http.MethodGet, "/api/admin/skills", nil)
	mock := findAdminSkillSummary(items, "mock-skill")
	if mock == nil || mock.Icon == "" || !strings.Contains(mock.Icon, "assets%2Fmock-skill.png") {
		t.Fatalf("expected mock-skill icon URL, got %#v", mock)
	}

	detailPath := "/api/admin/skills/detail?key=" + url.QueryEscape("mock-skill") + "&openPath=" + url.QueryEscape("SKILL.md")
	detail := getAPIData[api.AdminSkillDetailResponse](t, fixture.server, http.MethodGet, detailPath, nil)
	if detail.Skill.Key != "mock-skill" || detail.Skill.Icon == "" || detail.FileManifest.Revision == "" {
		t.Fatalf("unexpected detail: %#v", detail)
	}
	if detail.FileManifest.DefaultOpenPath != "SKILL.md" || detail.OpenedFile == nil || !strings.Contains(detail.OpenedFile.Content, "# Mock Skill") {
		t.Fatalf("expected lazy-opened SKILL.md, got detail=%#v opened=%#v", detail.FileManifest, detail.OpenedFile)
	}
	skillEntry := findAdminSkillEntryForTest(detail.FileManifest.Entries, "SKILL.md")
	if skillEntry == nil || skillEntry.Order != 0 || skillEntry.ParentPath != "" || skillEntry.ContentKind != "text" || skillEntry.Role != "skillMd" {
		t.Fatalf("unexpected SKILL.md entry: %#v", skillEntry)
	}

	binaryDetailPath := "/api/admin/skills/detail?key=" + url.QueryEscape("mock-skill") + "&openPath=" + url.QueryEscape("assets/logo.bin")
	binaryDetail := getAPIData[api.AdminSkillDetailResponse](t, fixture.server, http.MethodGet, binaryDetailPath, nil)
	if binaryDetail.OpenedFile != nil {
		t.Fatalf("binary or missing openPath should not inline content: %#v", binaryDetail.OpenedFile)
	}

	createBody := mustSkillJSON(t, api.CreateAdminSkillRequest{
		Key:     "helper-skill",
		SkillMd: "---\nname: Helper Skill\ndescription: Helps tests\n---\n\nUse carefully.\n",
		Files: []api.AdminSkillInlineFile{
			{Path: "references/guide.md", Content: "first version\n"},
		},
	})
	created := getAPIData[api.AdminSkillDetailResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/create", createBody)
	if created.Skill.Key != "helper-skill" || created.Skill.Name != "Helper Skill" || created.Skill.Icon != "" {
		t.Fatalf("unexpected create response: %#v", created)
	}
	guideEntry := findAdminSkillEntryForTest(created.FileManifest.Entries, "references/guide.md")
	if guideEntry == nil || guideEntry.ParentPath != "references" || guideEntry.Depth != 1 || guideEntry.Language != "markdown" || guideEntry.Role != "reference" {
		t.Fatalf("unexpected guide entry: %#v", guideEntry)
	}

	readPath := "/api/admin/skills/file?key=helper-skill&path=" + url.QueryEscape("references/guide.md")
	read := getAPIData[api.AdminSkillTextFile](t, fixture.server, http.MethodGet, readPath, nil)
	if read.Content != "first version\n" || read.SHA256 == "" || !read.Editable {
		t.Fatalf("unexpected file read: %#v", read)
	}

	writeBody := mustSkillJSON(t, api.WriteAdminSkillFileRequest{
		Key:        "helper-skill",
		Path:       "references/guide.md",
		Content:    "second version\n",
		BaseSHA256: read.SHA256,
	})
	written := getAPIData[api.AdminSkillMutationResponse](t, fixture.server, http.MethodPut, "/api/admin/skills/file", writeBody)
	if written.Action != "save" || written.OpenedFile == nil || written.OpenedFile.Content != "second version\n" || written.FileManifest != nil {
		t.Fatalf("unexpected write response: %#v", written)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/skills/file", bytes.NewReader(writeBody)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected stale base conflict, got %d: %s", rec.Code, rec.Body.String())
	}

	fileCreateBody := mustSkillJSON(t, api.CreateAdminSkillFileRequest{
		Key:     "helper-skill",
		Path:    "scripts/helper.py",
		Content: "print('ok')\n",
	})
	fileCreated := getAPIData[api.AdminSkillMutationResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/file/create", fileCreateBody)
	if fileCreated.Action != "create" || fileCreated.FileManifest == nil || fileCreated.SelectedPath != "scripts/helper.py" || fileCreated.OpenedFile == nil {
		t.Fatalf("unexpected file create response: %#v", fileCreated)
	}

	mkdirBody := mustSkillJSON(t, api.MkdirAdminSkillFileRequest{Key: "helper-skill", Path: "assets"})
	mkdir := getAPIData[api.AdminSkillMutationResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/file/mkdir", mkdirBody)
	if mkdir.Action != "mkdir" || mkdir.FileManifest == nil || mkdir.SelectedPath != "assets" {
		t.Fatalf("unexpected mkdir response: %#v", mkdir)
	}

	renameBody := mustSkillJSON(t, api.RenameAdminSkillFileRequest{Key: "helper-skill", FromPath: "references/guide.md", ToPath: "references/renamed.md"})
	renamed := getAPIData[api.AdminSkillMutationResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/file/rename", renameBody)
	if renamed.Action != "rename" || renamed.FileManifest == nil || renamed.SelectedPath != "references/renamed.md" {
		t.Fatalf("unexpected rename response: %#v", renamed)
	}

	deleteBody := mustSkillJSON(t, api.DeleteAdminSkillFileRequest{Key: "helper-skill", Path: "scripts/helper.py"})
	deleted := getAPIData[api.AdminSkillMutationResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/file/delete", deleteBody)
	if deleted.Action != "delete" || deleted.FileManifest == nil || deleted.SelectedPath != "SKILL.md" {
		t.Fatalf("unexpected delete response: %#v", deleted)
	}

	uploadBody, contentType := skillUploadBody(t, "helper-skill", "assets/helper-skill.png", []byte{0x89, 'P', 'N', 'G'})
	uploadRec := httptest.NewRecorder()
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/admin/skills/file/upload", uploadBody)
	uploadReq.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("upload expected 200, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}
	var uploadResp api.ApiResponse[api.AdminSkillMutationResponse]
	if err := json.Unmarshal(uploadRec.Body.Bytes(), &uploadResp); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	uploadedEntry := findAdminSkillEntryForTest(uploadResp.Data.FileManifest.Entries, "assets/helper-skill.png")
	if uploadResp.Data.Action != "upload" || uploadedEntry == nil || uploadedEntry.ContentKind != "binary" || uploadResp.Data.Skill == nil || uploadResp.Data.Skill.Icon == "" {
		t.Fatalf("unexpected upload response: %#v", uploadResp.Data)
	}

	downloadRec := httptest.NewRecorder()
	downloadPath := uploadResp.Data.Skill.Icon
	fixture.server.ServeHTTP(downloadRec, httptest.NewRequest(http.MethodGet, downloadPath, nil))
	if downloadRec.Code != http.StatusOK || !strings.Contains(downloadRec.Header().Get("Content-Type"), "image/png") {
		t.Fatalf("unexpected icon download status=%d content-type=%q", downloadRec.Code, downloadRec.Header().Get("Content-Type"))
	}

	deleteIconBody := mustSkillJSON(t, api.DeleteAdminSkillFileRequest{Key: "helper-skill", Path: "assets/helper-skill.png"})
	deletedIcon := getAPIData[api.AdminSkillMutationResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/file/delete", deleteIconBody)
	if deletedIcon.Skill == nil || deletedIcon.Skill.Icon != "" {
		t.Fatalf("expected icon to be omitted after deletion, got %#v", deletedIcon.Skill)
	}

	validateBody := mustSkillJSON(t, api.ValidateAdminSkillRequest{Key: "helper-skill"})
	validated := getAPIData[api.AdminSkillValidateResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/validate", validateBody)
	if validated.Key != "helper-skill" || validated.Status == "" {
		t.Fatalf("unexpected validate response: %#v", validated)
	}
}

func TestDeleteAdminSkillInUseReturnsConflict(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()
	body := mustSkillJSON(t, api.DeleteAdminSkillRequest{Key: "mock-skill"})
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/skills/delete", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "usedByAgents") || !strings.Contains(rec.Body.String(), "mock-agent") {
		t.Fatalf("expected usedByAgents in conflict response, got %s", rec.Body.String())
	}
}

func TestAdminSkillVersionField(t *testing.T) {
	fixture := newTestFixture(t)
	createSkill := func(key string, skillMd string) {
		t.Helper()
		body := mustSkillJSON(t, api.CreateAdminSkillRequest{Key: key, SkillMd: skillMd})
		created := getAPIData[api.AdminSkillDetailResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/create", body)
		if created.Skill.Key != key {
			t.Fatalf("unexpected create response: %#v", created)
		}
	}
	createSkill("version-top", "---\nname: Top Version\nversion: 0.0.0\n---\n\nBody\n")
	createSkill("version-meta", "---\nname: Meta Version\nmetadata:\n  version: 1.2.3\n---\n\nBody\n")
	createSkill("version-both", "---\nname: Both Version\nversion: 9.9.9\nmetadata:\n  version: 1.0.0\n---\n\nBody\n")
	createSkill("version-none", "---\nname: No Version\n---\n\nBody\n")

	items := getAPIData[[]api.AdminSkillSummary](t, fixture.server, http.MethodGet, "/api/admin/skills", nil)
	want := map[string]string{
		"version-top":  "0.0.0",
		"version-meta": "1.2.3",
		"version-both": "9.9.9",
		"version-none": "",
	}
	seen := map[string]bool{}
	for _, item := range items {
		expected, ok := want[item.Key]
		if !ok {
			continue
		}
		seen[item.Key] = true
		if item.Version != expected {
			t.Fatalf("skill %s Version = %q, want %q", item.Key, item.Version, expected)
		}
	}
	for key := range want {
		if !seen[key] {
			t.Fatalf("skill %s missing from list response", key)
		}
	}
}

func TestAdminSkillImportCreatesSkillAndMapsFailures(t *testing.T) {
	fixture := newTestFixture(t)
	archive := serverSkillImportZIP(t, map[string]string{
		"SKILL.md":            "---\nname: Imported Skill\ndescription: Imported from ZIP\n---\n\nUse it.\n",
		"references/guide.md": "guide\n",
	})
	body, contentType := skillImportBody(t, "imported-skill", "imported-skill.zip", archive)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected import 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[api.AdminSkillDetailResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if response.Data.Skill.Key != "imported-skill" || response.Data.Skill.Name != "Imported Skill" || response.Data.OpenedFile == nil {
		t.Fatalf("unexpected import response: %#v", response.Data)
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "imported-skill", "references", "guide.md")); err != nil {
		t.Fatalf("stat imported file: %v", err)
	}

	duplicateBody, duplicateType := skillImportBody(t, "imported-skill", "imported-skill.zip", archive)
	duplicate := httptest.NewRecorder()
	duplicateReq := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import", duplicateBody)
	duplicateReq.Header.Set("Content-Type", duplicateType)
	fixture.server.ServeHTTP(duplicate, duplicateReq)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("expected duplicate 409, got %d: %s", duplicate.Code, duplicate.Body.String())
	}

	invalidBody, invalidType := skillImportBody(t, "invalid-skill", "invalid-skill.zip", serverSkillImportZIP(t, map[string]string{"README.md": "missing skill"}))
	invalid := httptest.NewRecorder()
	invalidReq := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import", invalidBody)
	invalidReq.Header.Set("Content-Type", invalidType)
	fixture.server.ServeHTTP(invalid, invalidReq)
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), "diagnostics") || !strings.Contains(invalid.Body.String(), "missing_skill_md") {
		t.Fatalf("expected validation 422 with diagnostics, got %d: %s", invalid.Code, invalid.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "invalid-skill")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected invalid import to leave no directory, got %v", err)
	}

	nonZIPBody, nonZIPType := skillImportBody(t, "not-zip", "not-zip.zip", []byte("not a zip"))
	nonZIP := httptest.NewRecorder()
	nonZIPReq := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import", nonZIPBody)
	nonZIPReq.Header.Set("Content-Type", nonZIPType)
	fixture.server.ServeHTTP(nonZIP, nonZIPReq)
	if nonZIP.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected non-ZIP 415, got %d: %s", nonZIP.Code, nonZIP.Body.String())
	}

	wrongFieldBody, wrongFieldType := skillImportBodyWithFileFields(t, "wrong-field", "wrong-field.zip", archive, "archive")
	wrongField := httptest.NewRecorder()
	wrongFieldReq := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import", wrongFieldBody)
	wrongFieldReq.Header.Set("Content-Type", wrongFieldType)
	fixture.server.ServeHTTP(wrongField, wrongFieldReq)
	if wrongField.Code != http.StatusBadRequest {
		t.Fatalf("expected wrong file field 400, got %d: %s", wrongField.Code, wrongField.Body.String())
	}

	multipleBody, multipleType := skillImportBodyWithFileFields(t, "multiple-files", "multiple-files.zip", archive, "file", "file")
	multiple := httptest.NewRecorder()
	multipleReq := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import", multipleBody)
	multipleReq.Header.Set("Content-Type", multipleType)
	fixture.server.ServeHTTP(multiple, multipleReq)
	if multiple.Code != http.StatusBadRequest {
		t.Fatalf("expected multiple files 400, got %d: %s", multiple.Code, multiple.Body.String())
	}
}

func TestAdminSkillImportRollsBackWhenCatalogReloadFails(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.catalogReloader = failingSkillImportReloader{}
	fixture.server = newServerFromFixture(t, fixture)
	archive := serverSkillImportZIP(t, map[string]string{"SKILL.md": "# Rollback Skill\n"})
	body, contentType := skillImportBody(t, "rollback-skill", "rollback-skill.zip", archive)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/skills/import", body)
	req.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected reload failure 500, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "rollback-skill")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected imported directory rollback, got %v", err)
	}
}

func TestAdminSkillDownloadReturnsZipArchive(t *testing.T) {
	fixture := newTestFixture(t)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/skills/download?key=mock-skill", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected ZIP download 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/zip") {
		t.Fatalf("unexpected ZIP content type %q", contentType)
	}
	if contentDisposition := rec.Header().Get("Content-Disposition"); !strings.Contains(contentDisposition, "mock-skill.zip") {
		t.Fatalf("unexpected ZIP content disposition %q", contentDisposition)
	}
	reader, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("read ZIP response: %v", err)
	}
	entries := map[string]*zip.File{}
	for _, entry := range reader.File {
		entries[entry.Name] = entry
	}
	if entries["SKILL.md"] == nil || entries["assets/mock-skill.png"] == nil {
		t.Fatalf("unexpected ZIP entries: %#v", entries)
	}

	missingKey := httptest.NewRecorder()
	fixture.server.ServeHTTP(missingKey, httptest.NewRequest(http.MethodGet, "/api/admin/skills/download", nil))
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("expected missing key 400, got %d: %s", missingKey.Code, missingKey.Body.String())
	}

	missingSkill := httptest.NewRecorder()
	fixture.server.ServeHTTP(missingSkill, httptest.NewRequest(http.MethodGet, "/api/admin/skills/download?key=missing-skill", nil))
	if missingSkill.Code != http.StatusNotFound {
		t.Fatalf("expected missing skill 404, got %d: %s", missingSkill.Code, missingSkill.Body.String())
	}
}

func TestAdminSkillDownloadRejectsOversizedArchive(t *testing.T) {
	fixture := newTestFixture(t)
	oversized := filepath.Join(fixture.cfg.Paths.SkillsCenterDir, "mock-skill", "archive-too-large.bin")
	if err := os.WriteFile(oversized, nil, 0o644); err != nil {
		t.Fatalf("create oversized file: %v", err)
	}
	if err := os.Truncate(oversized, catalog.EditableSkillMaxArchiveBytes+1); err != nil {
		t.Fatalf("create sparse oversized file: %v", err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/skills/download?key=mock-skill", nil))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected ZIP download 413, got %d: %s", rec.Code, rec.Body.String())
	}
	var response api.ApiResponse[map[string]any]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode 413 response: %v", err)
	}
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected envelope code 413, got %#v", response)
	}
}

func TestAdminSkillDetailRequiresCanonicalKey(t *testing.T) {
	fixture := newTestFixture(t)

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/skills/detail?skillKey=mock-skill", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing canonical key 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "key is required") {
		t.Fatalf("expected canonical key error, got %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/skills/detail", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected missing key 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "key is required") {
		t.Fatalf("expected missing key error, got %s", rec.Body.String())
	}
}

func findAdminSkillSummary(items []api.AdminSkillSummary, key string) *api.AdminSkillSummary {
	for i := range items {
		if items[i].Key == key {
			return &items[i]
		}
	}
	return nil
}

func findAdminSkillEntryForTest(items []api.AdminSkillFileEntry, path string) *api.AdminSkillFileEntry {
	for i := range items {
		if items[i].Path == path {
			return &items[i]
		}
	}
	return nil
}

func mustSkillJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return data
}

func skillUploadBody(t *testing.T, key string, path string, data []byte) (io.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("key", key); err != nil {
		t.Fatalf("write key field: %v", err)
	}
	if err := writer.WriteField("path", path); err != nil {
		t.Fatalf("write path field: %v", err)
	}
	part, err := writer.CreateFormFile("file", "blob.bin")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &body, writer.FormDataContentType()
}

type failingSkillImportReloader struct{}

func (failingSkillImportReloader) Reload(context.Context, string) error {
	return errors.New("reload failed")
}

func serverSkillImportZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, content := range files {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create ZIP entry %s: %v", name, err)
		}
		if _, err := file.Write([]byte(content)); err != nil {
			t.Fatalf("write ZIP entry %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close skill import ZIP: %v", err)
	}
	return output.Bytes()
}

func skillImportBody(t *testing.T, key string, filename string, data []byte) (io.Reader, string) {
	return skillImportBodyWithFileFields(t, key, filename, data, "file")
}

func skillImportBodyWithFileFields(t *testing.T, key string, filename string, data []byte, fieldNames ...string) (io.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("key", key); err != nil {
		t.Fatalf("write import key: %v", err)
	}
	for _, fieldName := range fieldNames {
		part, err := writer.CreateFormFile(fieldName, filename)
		if err != nil {
			t.Fatalf("create import file: %v", err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatalf("write import file: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close import multipart: %v", err)
	}
	return &body, writer.FormDataContentType()
}
