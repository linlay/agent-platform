package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConnectorZIPImportHTTPConflictAndAuthContract(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for name, data := range map[string]string{"connector.json": `{"id":"import-demo","name":"Demo","version":"1.0.0","type":"mcp","auth_mode":"none"}`, "mcp.json": `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`} {
		f, _ := z.Create(name)
		f.Write([]byte(data))
	}
	z.Close()
	upload := func(overwrite string) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		f, _ := form.CreateFormFile("file", "demo.zip")
		f.Write(archive.Bytes())
		if overwrite != "" {
			form.WriteField("overwrite", overwrite)
		}
		form.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		return rec
	}
	for _, step := range []struct {
		overwrite string
		status    int
	}{{"", 200}, {"", 409}, {"true", 200}, {"not-bool", 400}} {
		rec := upload(step.overwrite)
		if rec.Code != step.status {
			t.Fatalf("import %q: %d %s", step.overwrite, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/connectors/auth?id=import-demo", nil))
	var response struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &response)
	if rec.Code != 200 || response.Data.Status != "not_required" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("auth status: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/auth?id=import-demo", nil))
	if rec.Code != 400 {
		t.Fatal("started login for auth none")
	}
}
