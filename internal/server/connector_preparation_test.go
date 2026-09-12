package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

func TestConnectorImportStartsIndependentPreparationAndCanCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX init fixture")
	}
	fixture := setupAdminRegistriesFixture(t)
	cli := map[string]any{"init": map[string]string{"darwin": "sleep 20", "linux": "sleep 20", "win32": "exit /b 0"}, "versionCheck": map[string]any{"command": map[string]string{"darwin": "demo --version", "linux": "demo --version", "win32": "demo.cmd --version"}, "minVersion": "1.0.0"}}
	data, _ := json.Marshal(cli)
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for name, content := range map[string]string{"connector.json": `{"id":"prepare-demo","name":"Demo","version":"1.0.0","type":"cli","auth_mode":null}`, "cli.json": string(data)} {
		f, _ := z.Create(name)
		f.Write([]byte(content))
	}
	z.Close()
	upload := func(overwrite bool) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		f, _ := form.CreateFormFile("file", "demo.zip")
		f.Write(archive.Bytes())
		if overwrite {
			form.WriteField("overwrite", "true")
		}
		form.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		return rec
	}
	start := time.Now()
	rec := upload(false)
	if rec.Code != 200 || time.Since(start) > 5*time.Second {
		t.Fatalf("async import: %d %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data struct {
			Installed   bool
			Preparation struct{ Status string }
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &response)
	if !response.Data.Installed || response.Data.Preparation.Status != "preparing" {
		t.Fatal(rec.Body.String())
	}
	defer fixture.server.connectorAuth.CancelPreparation("prepare-demo")
	if rec = upload(true); rec.Code != 409 {
		t.Fatalf("overwrite during preparation: %d %s", rec.Code, rec.Body.String())
	}
	call := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec
	}
	if rec = call(http.MethodGet, "/api/admin/connectors/prepare?id=prepare-demo"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if rec = call(http.MethodGet, "/api/admin/connectors"); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"preparation"`)) {
		t.Fatal(rec.Body.String())
	}
	if rec = call(http.MethodDelete, "/api/admin/connectors/prepare?id=prepare-demo"); rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"canceled"`)) {
		t.Fatal(rec.Body.String())
	}
	if rec = call(http.MethodPost, "/api/admin/connectors/prepare?id=prepare-demo"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
}
