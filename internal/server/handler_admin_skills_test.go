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

func TestAdminSkillsListDetailAndFileEditing(t *testing.T) {
	fixture := newTestFixture(t)

	items := getAPIData[[]api.AdminSkillSummary](t, fixture.server, http.MethodGet, "/api/admin/skills?tag=ignored", nil)
	mock := findAdminSkillSummary(items, "mock-skill")
	if mock == nil {
		t.Fatalf("expected mock-skill in admin skills: %#v", items)
	}
	if mock.Status != "ready" || len(mock.UsedByAgents) != 1 || mock.UsedByAgents[0] != "mock-agent" {
		t.Fatalf("unexpected mock skill summary: %#v", mock)
	}

	detailPath := "/api/admin/skills/detail?key=" + url.QueryEscape("mock-skill")
	detail := getAPIData[api.AdminSkillDetailResponse](t, fixture.server, http.MethodGet, detailPath, nil)
	if detail.Key != "mock-skill" || !strings.Contains(detail.SkillMd, "# Mock Skill") || len(detail.Files) == 0 {
		t.Fatalf("unexpected skill detail: %#v", detail)
	}

	createBody := mustSkillJSON(t, api.CreateAdminSkillRequest{
		Key:     "helper-skill",
		SkillMd: "---\nname: Helper Skill\ndescription: Helps tests\n---\n\nUse carefully.\n",
		Files: []api.AdminSkillInlineFile{
			{Path: "references/guide.md", Content: "first version\n"},
		},
	})
	created := getAPIData[api.AdminSkillDetailResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/create", createBody)
	if created.Key != "helper-skill" || created.Name != "Helper Skill" {
		t.Fatalf("unexpected create response: %#v", created)
	}

	readPath := "/api/admin/skills/file?key=helper-skill&path=" + url.QueryEscape("references/guide.md")
	read := getAPIData[api.AdminSkillFileResponse](t, fixture.server, http.MethodGet, readPath, nil)
	if read.Content != "first version\n" || read.SHA256 == "" {
		t.Fatalf("unexpected file read: %#v", read)
	}

	writeBody := mustSkillJSON(t, api.WriteAdminSkillFileRequest{
		Key:        "helper-skill",
		Path:       "references/guide.md",
		Content:    "second version\n",
		BaseSHA256: read.SHA256,
	})
	written := getAPIData[api.AdminSkillFileMutationResponse](t, fixture.server, http.MethodPut, "/api/admin/skills/file", writeBody)
	if !written.Updated || written.File == nil || written.File.SHA256 == read.SHA256 {
		t.Fatalf("unexpected write response: %#v", written)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/admin/skills/file", bytes.NewReader(writeBody)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected stale base conflict, got %d: %s", rec.Code, rec.Body.String())
	}

	mkdirBody := mustSkillJSON(t, api.MkdirAdminSkillFileRequest{Key: "helper-skill", Path: "scripts"})
	mkdir := getAPIData[api.AdminSkillFileMutationResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/file/mkdir", mkdirBody)
	if !mkdir.Created || mkdir.File == nil || mkdir.File.Kind != "directory" {
		t.Fatalf("unexpected mkdir response: %#v", mkdir)
	}

	renameBody := mustSkillJSON(t, api.RenameAdminSkillFileRequest{Key: "helper-skill", FromPath: "references/guide.md", ToPath: "references/renamed.md"})
	renamed := getAPIData[api.AdminSkillFileMutationResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/file/rename", renameBody)
	if !renamed.Renamed || renamed.ToPath != "references/renamed.md" {
		t.Fatalf("unexpected rename response: %#v", renamed)
	}

	deleteBody := mustSkillJSON(t, api.DeleteAdminSkillFileRequest{Key: "helper-skill", Path: "references/renamed.md"})
	deleted := getAPIData[api.AdminSkillFileMutationResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/file/delete", deleteBody)
	if !deleted.Deleted {
		t.Fatalf("unexpected delete response: %#v", deleted)
	}

	uploadBody, contentType := skillUploadBody(t, "helper-skill", "references/blob.bin", []byte{0, 1, 2, 3})
	uploadRec := httptest.NewRecorder()
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/admin/skills/file/upload", uploadBody)
	uploadReq.Header.Set("Content-Type", contentType)
	fixture.server.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("upload expected 200, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}
	var uploadResp api.ApiResponse[api.AdminSkillFileMutationResponse]
	if err := json.Unmarshal(uploadRec.Body.Bytes(), &uploadResp); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if uploadResp.Data.File == nil || !uploadResp.Data.File.Binary {
		t.Fatalf("expected binary upload metadata, got %#v", uploadResp.Data)
	}

	downloadRec := httptest.NewRecorder()
	downloadPath := "/api/admin/skills/file/download?key=helper-skill&path=" + url.QueryEscape("references/blob.bin")
	fixture.server.ServeHTTP(downloadRec, httptest.NewRequest(http.MethodGet, downloadPath, nil))
	if downloadRec.Code != http.StatusOK || !bytes.Equal(downloadRec.Body.Bytes(), []byte{0, 1, 2, 3}) {
		t.Fatalf("unexpected download status=%d body=%v", downloadRec.Code, downloadRec.Body.Bytes())
	}

	deleteSkillBody := mustSkillJSON(t, api.DeleteAdminSkillRequest{Key: "helper-skill"})
	deleteSkill := getAPIData[api.DeleteAdminSkillResponse](t, fixture.server, http.MethodPost, "/api/admin/skills/delete", deleteSkillBody)
	if !deleteSkill.Deleted {
		t.Fatalf("unexpected skill delete response: %#v", deleteSkill)
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

func findAdminSkillSummary(items []api.AdminSkillSummary, key string) *api.AdminSkillSummary {
	for i := range items {
		if items[i].Key == key {
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
