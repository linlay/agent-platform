package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-platform/internal/config"
)

func TestConnectorIconZIPCatalogAndHTTP(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path fill="#07c160" d="M0 0h24v24H0z"/></svg>`
	upload := func(icon string) *httptest.ResponseRecorder {
		var archive bytes.Buffer
		z := zip.NewWriter(&archive)
		for name, data := range map[string]string{
			"connector.json": `{"id":"brand-demo","name":"Brand","version":"1.1.1","type":"cli","auth_mode":null,"icon":"assets/icon.svg"}`,
			"cli.json":       `{}`, "assets/icon.svg": icon,
		} {
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
	if rec := upload(svg); rec.Code != http.StatusOK {
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
			if item.Icon != "assets/icon.svg" || !strings.HasPrefix(item.IconURL, "/api/connectors/icon?id=brand-demo&v=") || len(item.IconSHA256) != 64 {
				t.Fatalf("missing icon metadata: %+v", item)
			}
			image := request(item.IconURL, "")
			if image.Code != http.StatusOK || image.Body.String() != svg || image.Header().Get("Content-Type") != "image/svg+xml" || image.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(image.Header().Get("Content-Security-Policy"), "sandbox") {
				t.Fatalf("icon response: %d %s", image.Code, image.Body.String())
			}
			if cached := request(item.IconURL, image.Header().Get("ETag")); cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
				t.Fatal("icon ETag did not return 304")
			}
		}
		if !found {
			t.Fatal("imported brand connector missing")
		}
	}
	if rec := upload(`<svg onload="alert(1)"/>`); rec.Code != http.StatusBadRequest {
		t.Fatalf("active SVG accepted: %d", rec.Code)
	}
	if rec := request("/api/connectors/icon?id=brand-demo&file=../../.state/credentials.json", ""); rec.Body.String() != svg {
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
