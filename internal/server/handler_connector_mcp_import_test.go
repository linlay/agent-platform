package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installInvalidConnectorFixture(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "tencent-map")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"connector.json":    `{"id":"tencent-map","name":"Map","version":"1.0.0","type":"mcp","authMode":"token"}`,
		"token-schema.json": `{"title_en":"Map","fields":[{"key":"TOKEN"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConnectorMCPImportIsolatesInvalidInstalledPackage(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	installInvalidConnectorFixture(t, f.cfg.Paths.EffectiveConnectorsCenterDir())
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", strings.NewReader(`{"name":"127.0.0.1","id":"127.0.0.1","mcpUrl":"http://127.0.0.1:8081/mcp"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("invalid unrelated package blocked import: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/connectors", nil))
	var response struct {
		Data struct {
			Connectors []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				Error  string `json:"error"`
				HasMCP bool   `json:"hasMcp"`
			} `json:"connectors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("admin catalog: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	foundInvalid, foundImported := false, false
	for _, item := range response.Data.Connectors {
		if item.ID == "tencent-map" {
			foundInvalid = item.Status == "invalid" && strings.Contains(item.Error, `unknown field "title_en"`) && !item.HasMCP
		}
		if item.ID == "127.0.0.1" {
			foundImported = item.Status == "" && item.HasMCP
		}
	}
	if !foundInvalid || !foundImported {
		t.Fatalf("missing isolated diagnostic or imported package: %s", rec.Body.String())
	}
}

func TestConnectorMCPImportCreatesHTTPMCPPackage(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		fixture.server.ServeHTTP(rec, req)
		return rec
	}

	rec := post(`{"name":"mcp.example.test","id":"mcp.example.test","mcpUrl":"https://mcp.example.test/mcp"}`)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"authMode":"mcp"`)) {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	dir := filepath.Join(fixture.cfg.Paths.EffectiveConnectorsCenterDir(), "mcp.example.test")
	mcpJSON, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if err != nil || !bytes.Contains(mcpJSON, []byte("https://mcp.example.test/mcp")) || !bytes.Contains(mcpJSON, []byte("streamableHttp")) {
		t.Fatalf("generated mcp.json: %v %s", err, mcpJSON)
	}
	if _, err := os.Stat(filepath.Join(dir, "connector.json")); err != nil {
		t.Fatalf("generated connector.json: %v", err)
	}

	if rec := post(`{"name":"localhost","id":"localhost","mcpUrl":"http://localhost:8080/mcp"}`); rec.Code != http.StatusOK {
		t.Fatalf("localhost HTTP import: %d %s", rec.Code, rec.Body.String())
	}

	for _, body := range []string{
		`{"name":"mcp.example.test","id":"mcp.example.test","mcpUrl":"http://mcp.example.test/mcp"}`,
		`{"name":"mcp.example.test","id":"mcp.example.test","mcpUrl":"https://user:secret@mcp.example.test/mcp"}`,
		`{"name":"mcp.example.test","id":"mcp.example.test","mcpUrl":"https://mcp.example.test/mcp#fragment"}`,
		`{"name":"mcp.example.test","id":"mcp.example.test","mcpUrl":"ftp://mcp.example.test/mcp"}`,
		`{"name":"builtin.example.test","id":"builtin.example.test","mcpUrl":"https://builtin.example.test/mcp"}`,
	} {
		if rec := post(body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}

	rec = post(`{"name":"mcp.example.test","id":"mcp.example.test","mcpUrl":"https://mcp.example.test/other"}`)
	if rec.Code != http.StatusConflict || !bytes.Contains(rec.Body.Bytes(), []byte("connector_exists")) {
		t.Fatalf("duplicate host: %d %s", rec.Code, rec.Body.String())
	}
	after, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	if err != nil || !bytes.Equal(after, mcpJSON) {
		t.Fatalf("duplicate host changed the installed package: %v %s", err, after)
	}
}

func TestConnectorMCPImportExplicitNoAuth(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", strings.NewReader(`{"name":"private.example.test","id":"private.example.test","mcpUrl":"http://private.example.test/mcp","authMode":"none"}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"authMode":"none"`) {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/connectors/connection?id=private.example.test", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"readiness":"no_auth"`) {
		t.Fatalf("no-auth readiness: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConnectorMCPImportRejectsInvalidRequestsBeforePublication(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	reloader := &recordingServerCatalogReloader{}
	f.server.deps.CatalogReloader = reloader
	for _, body := range []string{
		`null`, `{}`, `{"mcpUrl":null}`,
		`{"MCPURL":"https://demo.example.test/mcp"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","mcpUrl":"https://other.example.test/mcp"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","overwrite":true}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","Name":"Demo"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","name":42}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","name":"First","name":"Second"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","AuthMode":"none"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","authMode":null}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","authMode":""}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","authMode":"self"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","authMode":"token","tokenAuth":"unknown"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","authMode":"token","tokenAuth":"header","headerName":"Host"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","authMode":"token","tokenAuth":"header","headerName":"X-Key\\r\\nOther"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","authMode":"none","tokenAuth":"bearer"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp","authMode":"oneid","headerName":"X-Key"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"http://demo.example.test/mcp","authMode":"oneid"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test:8443/mcp","authMode":"oneid"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp#"}`,
		`{"name":"demo.example.test","id":"demo.example.test","mcpUrl":"https://demo.example.test/mcp"} {}`,
	} {
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(reloader.reasons) != 0 {
		t.Fatalf("invalid requests caused reload: %v", reloader.reasons)
	}
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", strings.NewReader(strings.Repeat(" ", connectorMCPImportMaxBytes+1))))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize request: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConnectorMCPImportRollsBackFailedReload(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	f.server.deps.CatalogReloader = &recordingServerCatalogReloader{err: errServerCatalogReload}
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", strings.NewReader(`{"name":"rollback.example.test","id":"rollback.example.test","mcpUrl":"https://rollback.example.test/mcp"}`)))
	if rec.Code == http.StatusOK {
		t.Fatalf("accepted failed reload: %s", rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Paths.EffectiveConnectorsCenterDir(), "rollback.example.test")); !os.IsNotExist(err) {
		t.Fatalf("failed import retained published package: %v", err)
	}
}

func TestConnectorMCPImportAuthenticationModes(t *testing.T) {
	for _, tc := range []struct {
		mode, kind, header, wantHeader, wantTemplate string
		fields                                       int
	}{
		{"oneid", "", "", "", "", 0},
		{"token", "", "", "Authorization", "Bearer ${TOKEN}", 1},
		{"token", "bearer", "", "Authorization", "Bearer ${TOKEN}", 1},
		{"token", "header", "X-API-Key", "X-API-Key", "${TOKEN}", 1},
		{"token", "basic", "", "Authorization", "Basic ${USERNAME}:${PASSWORD}", 2},
	} {
		t.Run(tc.mode+tc.kind, func(t *testing.T) {
			f := setupAdminRegistriesFixture(t)
			input := map[string]string{"name": "Authentication", "id": "auth.example.test", "mcpUrl": "https://auth.example.test/mcp", "authMode": tc.mode}
			if tc.kind != "" {
				input["tokenAuth"] = tc.kind
			}
			if tc.header != "" {
				input["headerName"] = tc.header
			}
			body, _ := json.Marshal(input)
			rec := httptest.NewRecorder()
			f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", bytes.NewReader(body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
			}
			dir := filepath.Join(f.cfg.Paths.EffectiveConnectorsCenterDir(), "auth.example.test")
			raw, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
			var config struct {
				MCPServers map[string]struct{ Headers map[string]string }
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			if tc.wantHeader != "" && config.MCPServers["main"].Headers[tc.wantHeader] != tc.wantTemplate {
				t.Fatalf("wrong credential template: %s", raw)
			}
			raw, err = os.ReadFile(filepath.Join(dir, "token-schema.json"))
			if tc.fields == 0 {
				if !os.IsNotExist(err) {
					t.Fatalf("unexpected token schema: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				Fields []struct {
					Key, Type string
					Required  bool
				}
			}
			if err := json.Unmarshal(raw, &schema); err != nil || len(schema.Fields) != tc.fields {
				t.Fatalf("token schema: %s %v", raw, err)
			}
			for _, field := range schema.Fields {
				if !field.Required || field.Type != "password" && field.Key != "USERNAME" {
					t.Fatalf("invalid credential form: %s", raw)
				}
			}
			rec = httptest.NewRecorder()
			f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/connectors", nil))
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"tokenSchema"`) {
				t.Fatalf("credential form missing from catalog: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestConnectorMCPImportMetadata(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	post := func(input map[string]string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(input)
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", bytes.NewReader(raw)))
		return rec
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, id, wantID, description, preset, png string }{
		{name: "  Custom Tool  ", wantID: "local-8080", description: "  Custom tools  ", preset: "database"},
		{name: "团队知识库", id: "team-knowledge", wantID: "team-knowledge", png: base64.StdEncoding.EncodeToString(pngData.Bytes())},
	} {
		rec := post(map[string]string{"name": tc.name, "id": tc.id, "description": tc.description, "iconPreset": tc.preset, "iconPng": tc.png, "mcpUrl": "http://127.0.0.1:8080/mcp", "authMode": "none"})
		if rec.Code != http.StatusOK {
			t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
		}
		var result struct{ Data struct{ ID, Name string } }
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Data.ID != tc.wantID || result.Data.Name != strings.TrimSpace(tc.name) {
			t.Fatalf("identity: %s", rec.Body.String())
		}
		raw, err := os.ReadFile(filepath.Join(f.cfg.Paths.EffectiveConnectorsCenterDir(), tc.wantID, "connector.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct{ Name, Description, Icon string }
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest.Name != strings.TrimSpace(tc.name) || manifest.Description != strings.TrimSpace(tc.description) || manifest.Icon == "" {
			t.Fatalf("metadata: %s", raw)
		}
		icon, err := os.ReadFile(filepath.Join(f.cfg.Paths.EffectiveConnectorsCenterDir(), tc.wantID, manifest.Icon))
		if err != nil || len(icon) == 0 {
			t.Fatalf("icon: %v", err)
		}
		rec = httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/connectors/icon?id="+tc.wantID, nil))
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), icon) {
			t.Fatalf("icon read: %d %s", rec.Code, rec.Body.String())
		}
	}
	// Both connectors use the exact same localhost URL; identity is name/ID based.
	rec := post(map[string]string{"name": "Custom Tool", "id": "local-8080", "mcpUrl": "https://different.example.test/mcp", "authMode": "none"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("same name must not overwrite: %d %s", rec.Code, rec.Body.String())
	}
	for _, input := range []map[string]string{
		{}, {"name": " "}, {"name": "Tool", "id": "../escape"}, {"name": "Tool", "id": "builtin.foo"},
		{"name": "Tool", "iconPng": "not base64"}, {"name": "Tool", "iconPng": base64.StdEncoding.EncodeToString([]byte("not a PNG"))},
		{"name": "Tool", "iconPng": base64.StdEncoding.EncodeToString(make([]byte, 32*1024+1))},
		{"name": "Tool", "iconPreset": "unknown"}, {"name": "Tool", "iconPreset": "hub", "iconPng": base64.StdEncoding.EncodeToString(pngData.Bytes())},
	} {
		input["mcpUrl"] = "https://invalid.example.test/mcp"
		input["authMode"] = "none"
		rec := post(input)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid metadata accepted: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestConnectorMCPImportAllPresetIcons(t *testing.T) {
	for _, preset := range []string{"hub", "database", "terminal", "code", "folder", "search", "bolt", "build"} {
		t.Run(preset, func(t *testing.T) {
			f := setupAdminRegistriesFixture(t)
			body, _ := json.Marshal(map[string]string{"name": "Preset " + preset, "iconPreset": preset, "mcpUrl": "https://icon.example.test/mcp"})
			rec := httptest.NewRecorder()
			f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", bytes.NewReader(body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("preset: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestConnectorMCPImportLargeIcon(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	icon := image.NewNRGBA(image.Rect(0, 0, 80, 80))
	if _, err := rand.New(rand.NewSource(1)).Read(icon.Pix); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, icon); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() <= 10*1024 || encoded.Len() > 32*1024 {
		t.Fatalf("unexpected test icon size: %d", encoded.Len())
	}
	body, _ := json.Marshal(map[string]string{"name": "Large Icon", "mcpUrl": "https://icon.example.test/mcp", "iconPng": base64.StdEncoding.EncodeToString(encoded.Bytes())})
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("large icon import: %d %s", rec.Code, rec.Body.String())
	}
	saved, err := os.ReadFile(filepath.Join(f.cfg.Paths.EffectiveConnectorsCenterDir(), "icon-example-test", "assets", "icon.png"))
	if err != nil || !bytes.Equal(saved, encoded.Bytes()) {
		t.Fatalf("saved icon differs: %v", err)
	}
}

func TestConnectorMCPImportURLIdentity(t *testing.T) {
	for _, tc := range []struct{ url, want string }{
		{"https://example.com/mcp?key=private", "example-com"},
		{"http://127.0.0.1:8080/mcp", "local-8080"},
		{"http://localhost:3000/sse", "local-3000"},
		{"http://[::1]:8080/mcp", "local-8080"},
		{"https://example.com:443/mcp", "example-com"},
		{"https://example.com:8443/mcp", "example-com-8443"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			f := setupAdminRegistriesFixture(t)
			body, _ := json.Marshal(map[string]string{"name": "中文名称", "mcpUrl": tc.url, "authMode": "none"})
			rec := httptest.NewRecorder()
			f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import-mcp", bytes.NewReader(body)))
			var result struct{ Data struct{ ID string } }
			err := json.Unmarshal(rec.Body.Bytes(), &result)
			if rec.Code != http.StatusOK || err != nil || result.Data.ID != tc.want {
				t.Fatalf("URL identity: %d %s %v", rec.Code, rec.Body.String(), err)
			}
		})
	}
}
