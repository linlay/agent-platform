package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"agent-platform/internal/api"
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
