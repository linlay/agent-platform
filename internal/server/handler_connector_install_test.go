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

	"agent-platform/internal/connector"
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

func connectorIdentityTestArchive(t *testing.T, id, version, kind string) []byte {
	t.Helper()
	manifest, err := json.Marshal(map[string]string{"id": id, "name": "Identity Test", "version": version, "type": kind, "authMode": "none"})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"connector.json": string(manifest), "payload.txt": version}
	if kind == "cli" {
		files["cli.json"] = `{}`
	} else {
		files["mcp.json"] = `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`
	}
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	for name, content := range files {
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
	return data.Bytes()
}

func connectorIdentityTestRequest(t *testing.T, archive []byte, fields map[string][]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	entry, err := form.CreateFormFile("file", "connector.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(archive); err != nil {
		t.Fatal(err)
	}
	for name, values := range fields {
		for _, value := range values {
			if err := form.WriteField(name, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	return req
}

func TestConnectorZIPImportOptionalExpectedIdentity(t *testing.T) {
	const id, version = "identity-demo", "1.2.3-beta.1+build.7"
	for _, test := range []struct {
		name   string
		fields map[string][]string
	}{
		{"local import", nil},
		{"expected id only", map[string][]string{"expectedId": {id}}},
		{"expected version only", map[string][]string{"expectedVersion": {version}}},
		{"both expectations", map[string][]string{"expectedId": {id}, "expectedVersion": {version}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := setupAdminRegistriesFixture(t)
			reloader := &recordingServerCatalogReloader{}
			fixture.server.deps.CatalogReloader = reloader
			archive := connectorIdentityTestArchive(t, id, version, "mcp")
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, connectorIdentityTestRequest(t, archive, test.fields))
			if rec.Code != http.StatusOK {
				t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
			}
			pkg, err := fixture.cfg.Paths.ConnectorSources().Load(id)
			if err != nil || pkg.Version != version {
				t.Fatalf("installed package: version=%q error=%v", pkg.Version, err)
			}
			if len(reloader.reasons) != 1 || reloader.reasons[0] != "connectors" {
				t.Fatalf("reloads: %v", reloader.reasons)
			}
		})
	}
}

func TestConnectorZIPImportExpectedIdentityRejectsBeforeOverwrite(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	sources := fixture.cfg.Paths.ConnectorSources()
	reloader := &recordingServerCatalogReloader{}
	fixture.server.deps.CatalogReloader = reloader
	const id, otherID = "identity-actual", "identity-expected"
	originals := map[string]string{}
	for _, packageID := range []string{id, otherID} {
		archive := connectorIdentityTestArchive(t, packageID, "0.9.0", "cli")
		pkg, err := connector.ImportArchive(t.Context(), sources, bytes.NewReader(archive), int64(len(archive)), false, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		originals[packageID], err = connector.RuntimeFingerprint(pkg.Dir)
		if err != nil {
			t.Fatal(err)
		}
	}
	archive := connectorIdentityTestArchive(t, id, "1.0.0", "cli")
	for _, test := range []struct {
		name, code string
		status     int
		fields     map[string][]string
	}{
		{"different id", "connector_identity_mismatch", http.StatusConflict, map[string][]string{"expectedId": {otherID}, "expectedVersion": {"1.0.0"}}},
		{"different version", "connector_identity_mismatch", http.StatusConflict, map[string][]string{"expectedId": {id}, "expectedVersion": {"2.0.0"}}},
		{"different build metadata", "connector_identity_mismatch", http.StatusConflict, map[string][]string{"expectedVersion": {"1.0.0+build.1"}}},
		{"empty id", "invalid_connector", http.StatusBadRequest, map[string][]string{"expectedId": {""}}},
		{"invalid id", "invalid_connector", http.StatusBadRequest, map[string][]string{"expectedId": {"../identity-actual"}}},
		{"id whitespace", "invalid_connector", http.StatusBadRequest, map[string][]string{"expectedId": {" " + id}}},
		{"empty version", "invalid_connector", http.StatusBadRequest, map[string][]string{"expectedVersion": {""}}},
		{"invalid version", "invalid_connector", http.StatusBadRequest, map[string][]string{"expectedVersion": {"v1.0.0"}}},
		{"invalid prerelease", "invalid_connector", http.StatusBadRequest, map[string][]string{"expectedVersion": {"1.0.0-01"}}},
		{"version whitespace", "invalid_connector", http.StatusBadRequest, map[string][]string{"expectedVersion": {"1.0.0 "}}},
		{"duplicate id", "invalid_connector", http.StatusBadRequest, map[string][]string{"expectedId": {id, id}}},
		{"duplicate version", "invalid_connector", http.StatusBadRequest, map[string][]string{"expectedVersion": {"1.0.0", "1.0.0"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.fields["overwrite"] = []string{"true"}
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, connectorIdentityTestRequest(t, archive, test.fields))
			if rec.Code != test.status || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"`+test.code+`"`)) {
				t.Fatalf("rejection: %d %s", rec.Code, rec.Body.String())
			}
			for packageID, original := range originals {
				got, err := connector.RuntimeFingerprint(filepath.Join(sources.ExternalRoot, packageID))
				if err != nil || got != original {
					t.Fatalf("package %s changed after rejection: fingerprint=%s error=%v", packageID, got, err)
				}
				preparationPath := filepath.Join(sources.InstallRoot(), packageID, "preparation.json")
				if _, err := os.Stat(preparationPath); !os.IsNotExist(err) {
					t.Fatalf("rejected import started CLI preparation: %v", err)
				}
			}
			if len(reloader.reasons) != 0 {
				t.Fatalf("rejected import triggered reload: %v", reloader.reasons)
			}
		})
	}
}

func TestConnectorZIPImportIdentityMismatchDoesNotInstall(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	reloader := &recordingServerCatalogReloader{}
	fixture.server.deps.CatalogReloader = reloader
	const id = "identity-new"
	archive := connectorIdentityTestArchive(t, id, "1.0.0", "cli")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, connectorIdentityTestRequest(t, archive, map[string][]string{"expectedId": {id}, "expectedVersion": {"2.0.0"}}))
	if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte(`"code":"connector_identity_mismatch"`)) {
		t.Fatalf("rejection: %d %s", rec.Code, rec.Body.String())
	}
	for _, dir := range []string{filepath.Join(fixture.cfg.Paths.EffectiveConnectorsCenterDir(), id), filepath.Join(fixture.cfg.Paths.EffectiveConnectorStateDir(), id), filepath.Join(fixture.cfg.Paths.ConnectorSources().InstallRoot(), id)} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("rejected import created installed package or preparation state: %s: %v", dir, err)
		}
	}
	if len(reloader.reasons) != 0 {
		t.Fatalf("rejected import triggered reload: %v", reloader.reasons)
	}
}
