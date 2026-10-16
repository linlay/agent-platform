package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func TestConnectorIconZIPCatalogAndHTTP(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="#07c160" d="M0 0h24v24H0z"/></svg>`
	for _, tc := range []struct {
		name, declared, path, data, mediaType string
	}{
		{"declared", "assets/custom.svg", "assets/custom.svg", svg, "image/svg+xml"},
		{"default SVG", "", "assets/icon.svg", svg, "image/svg+xml"},
		{"default PNG", "", "assets/icon.png", encoded.String(), "image/png"},
		{"ID SVG", "", "assets/brand-demo.svg", svg, "image/svg+xml"},
		{"ID PNG", "", "assets/brand-demo.png", encoded.String(), "image/png"},
		{"no icon", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testConnectorIconZIPCatalogAndHTTP(t, tc.declared, tc.path, tc.data, tc.mediaType)
		})
	}
}

func testConnectorIconZIPCatalogAndHTTP(t *testing.T, declared, iconPath, iconData, mediaType string) {
	t.Helper()
	fixture := setupAdminRegistriesFixture(t)
	fields := map[string]string{"id": "brand-demo", "name": "Brand", "version": "1.1.1", "type": "mcp", "authMode": "none"}
	if declared != "" {
		fields["icon"] = declared
	}
	manifest, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	upload := func(icon string) *httptest.ResponseRecorder {
		var archive bytes.Buffer
		z := zip.NewWriter(&archive)
		files := map[string]string{
			"connector.json": string(manifest),
			"mcp.json":       `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`,
		}
		if iconPath != "" {
			files[iconPath] = icon
		}
		for name, data := range files {
			f, err := z.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte(data)); err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, err := form.CreateFormFile("file", "brand-demo.zip")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(archive.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := form.WriteField("overwrite", "true"); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		return rec
	}
	if rec := upload(iconData); rec.Code != http.StatusOK {
		t.Fatalf("icon ZIP import failed: %d %s", rec.Code, rec.Body.String())
	}
	request := func(target, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		return rec
	}
	var previousURL, previousETag string
	for _, endpoint := range []string{"/api/connectors", "/api/admin/connectors"} {
		rec := request(endpoint, "")
		var response struct {
			Data struct {
				Connectors []struct {
					ID, Icon, IconURL, IconSHA256 string
				}
			}
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("catalog: %s", rec.Body.String())
		}
		found := false
		for _, item := range response.Data.Connectors {
			if item.ID != "brand-demo" {
				continue
			}
			found = true
			if iconPath == "" {
				if item.IconURL != "" || item.Icon != "" || item.IconSHA256 != "" {
					t.Fatalf("unexpected icon metadata: %+v", item)
				}
				continue
			}
			if !strings.HasPrefix(item.IconURL, "/api/connectors/icon?id=brand-demo&v=") {
				t.Fatalf("missing icon URL: %+v", item)
			}
			if endpoint == "/api/admin/connectors" && (item.Icon != iconPath || len(item.IconSHA256) != 64) {
				t.Fatalf("missing icon metadata: %+v", item)
			}
			if endpoint == "/api/connectors" && (item.Icon != "" || item.IconSHA256 != "") {
				t.Fatalf("usage catalog exposes unused icon metadata: %+v", item)
			}
			image := request(item.IconURL, "")
			if image.Code != http.StatusOK || image.Body.String() != iconData || image.Header().Get("Content-Type") != mediaType || image.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(image.Header().Get("Content-Security-Policy"), "sandbox") || image.Header().Get("Cache-Control") != "private, max-age=0, must-revalidate" {
				t.Fatalf("icon response: %d %s", image.Code, image.Body.String())
			}
			if cached := request(item.IconURL, image.Header().Get("ETag")); cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
				t.Fatal("icon ETag did not return 304")
			}
			previousURL, previousETag = item.IconURL, image.Header().Get("ETag")
		}
		if !found {
			t.Fatal("imported brand connector missing")
		}
	}
	definition := request("/api/admin/connectors/detail?id=brand-demo&file=connector.json", "")
	var saved struct {
		Data struct{ Content string }
	}
	if err := json.Unmarshal(definition.Body.Bytes(), &saved); err != nil || definition.Code != http.StatusOK || saved.Data.Content != string(manifest) {
		t.Fatalf("import rewrote manifest: %s, %v", definition.Body.String(), err)
	}
	if iconPath == "" {
		if rec := request("/api/connectors/icon?id=brand-demo", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("connector without icon: %d", rec.Code)
		}
		return
	}
	updated := iconData + "\n"
	if rec := upload(updated); rec.Code != http.StatusOK {
		t.Fatalf("updated icon ZIP import failed: %d %s", rec.Code, rec.Body.String())
	}
	image := request(previousURL, previousETag)
	if image.Code != http.StatusOK || image.Body.String() != updated || image.Header().Get("ETag") == previousETag {
		t.Fatalf("icon cache did not refresh: %d %s", image.Code, image.Body.String())
	}
	catalog := request("/api/admin/connectors", "")
	var body struct {
		Data struct {
			Connectors []struct{ ID, IconURL string }
		}
	}
	if err := json.Unmarshal(catalog.Body.Bytes(), &body); err != nil || catalog.Code != http.StatusOK {
		t.Fatalf("updated catalog: %s, %v", catalog.Body.String(), err)
	}
	found := false
	for _, item := range body.Data.Connectors {
		if item.ID != "brand-demo" {
			continue
		}
		found = true
		if item.IconURL == previousURL || !strings.HasSuffix(item.IconURL, "&v="+strings.Trim(image.Header().Get("ETag"), `"`)) {
			t.Fatalf("catalog icon URL did not refresh: %+v", item)
		}
	}
	if !found {
		t.Fatal("updated connector missing from catalog")
	}
	if rec := upload(`<svg onload="alert(1)"/>`); rec.Code != http.StatusBadRequest {
		t.Fatalf("active SVG accepted: %d", rec.Code)
	}
	if rec := request("/api/connectors/icon?id=brand-demo&file=../../.state/credentials.json", ""); rec.Body.String() != updated {
		t.Fatal("icon endpoint served an arbitrary file or invalid import replaced the previous image")
	}
	if rec := request("/api/connectors/icon?id=absent", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing icon: %d", rec.Code)
	}
	if rec := request("/api/connectors/icon?id=..", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid id: %d", rec.Code)
	}
}

func TestConnectorIconRequiresConfiguredAuthentication(t *testing.T) {
	fixture := newTestFixture(t)
	_, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{
		Enabled: true, LocalPublicKeyFile: publicKeyPath, Issuer: "agent-platform-local",
	}
	server := newServerFromFixture(t, fixture)
	req := httptest.NewRequest(http.MethodGet, "/api/connectors/icon?id=brand-demo", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("icon endpoint bypassed configured authentication: %d", rec.Code)
	}
}
