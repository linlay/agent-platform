package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestConnectorZIPImportHTTPConflictAndAuthContract(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for name, data := range map[string]string{"connector.json": `{"id":"import-demo","name":"Demo","version":"1.0.0","type":"mcp","authMode":"self"}`, "mcp.json": `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`} {
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
	if rec.Code != 200 || response.Data.Status != "delegated" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("auth status: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/auth?id=import-demo", nil))
	if rec.Code != 400 {
		t.Fatal("started login for auth none")
	}
}

func TestConnectorZIPImportChecksExpectedIdentityBeforePublication(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for name, content := range map[string]string{
		"connector.json": `{"id":"expected-demo","name":"Demo","version":"1.0.0","type":"mcp","authMode":"none"}`,
		"mcp.json":       `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`,
	} {
		entry, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	for _, fields := range []map[string]string{{"expectedId": "other"}, {"expectedVersion": "2.0.0"}, {"expectedId": "expected-demo", "expectedVersion": "1.0.0"}} {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for name, value := range fields {
			if err := form.WriteField(name, value); err != nil {
				t.Fatal(err)
			}
		}
		entry, err := form.CreateFormFile("file", "download.zip")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(archive.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if len(fields) == 1 {
			if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte("connector_identity_mismatch")) {
				t.Fatalf("identity mismatch: %d %s", rec.Code, rec.Body.String())
			}
			if _, err := os.Stat(filepath.Join(fixture.cfg.Paths.EffectiveConnectorsCenterDir(), "expected-demo")); !os.IsNotExist(err) {
				t.Fatal("mismatched package published")
			}
		} else if rec.Code != http.StatusOK {
			t.Fatalf("matching identity: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestOneIDConnectorZIPAuthorizationTemplateVariants(t *testing.T) {
	for _, template := range []string{"Bearer ${ONEID_TOKEN}", "Bearer ${AP_ACCESS_TOKEN}", ""} {
		t.Run(template, func(t *testing.T) {
			fixture := setupAdminRegistriesFixture(t)
			component := map[string]any{"type": "streamableHttp", "url": "https://example.test/mcp"}
			if template != "" {
				component["headers"] = map[string]string{"Authorization": template}
			}
			data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"main": component}})
			var archive bytes.Buffer
			z := zip.NewWriter(&archive)
			for name, content := range map[string]string{"connector.json": `{"id":"oneid-templates","name":"OneID Templates","version":"1.0.0","type":"mcp","authMode":"oneid-token"}`, "mcp.json": string(data)} {
				entry, err := z.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = entry.Write([]byte(content)); err != nil {
					t.Fatal(err)
				}
			}
			if err := z.Close(); err != nil {
				t.Fatal(err)
			}
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			entry, err := form.CreateFormFile("file", "oneid.zip")
			if err != nil {
				t.Fatal(err)
			}
			entry.Write(archive.Bytes())
			form.Close()
			req := httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import", &body)
			req.Header.Set("Content-Type", form.FormDataContentType())
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("import failed: %d %s", rec.Code, rec.Body.String())
			}
			source, err := os.ReadFile(filepath.Join(fixture.cfg.Paths.EffectiveConnectorsCenterDir(), "oneid-templates", "mcp.json"))
			if err != nil || !bytes.Equal(source, data) {
				t.Fatalf("source package rewritten: %v", err)
			}
		})
	}
}
