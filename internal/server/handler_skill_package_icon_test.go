package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
)

func TestSkillPackageRootIcon(t *testing.T) {
	f := newAgentSkillsTestFixture(t, false)
	writeProjectionPackage(t, f, "doc")
	root := filepath.Join(f.cfg.Paths.SkillsCenterDir, "office")
	url := "/api/skill-packages/icon?key=office"
	request := func(status int, mediaType string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		f.server.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != status || mediaType != "" && rec.Header().Get("Content-Type") != mediaType {
			t.Fatalf("icon: %d %s", rec.Code, rec.Body.String())
		}
		return rec
	}
	// Member and package assets icons are not package root icons.
	writeAgentSkillIconPNG(t, f.cfg.Paths.SkillsCenterDir, "office", 90)
	request(404, "")
	if err := os.Rename(filepath.Join(root, "assets", "office.png"), filepath.Join(root, "icon.png")); err != nil {
		t.Fatal(err)
	}
	png := request(200, "image/png").Body.Bytes()
	for _, path := range []string{"/api/skills", "/api/admin/skill-packages"} {
		if path == "/api/skills" {
			result := getAPIData[api.AgentSkillsResponse](t, f.server, "GET", path, nil)
			if len(result.Packages) != 1 || result.Packages[0].Icon != url {
				t.Fatalf("packages: %+v", result.Packages)
			}
		} else {
			result := getAPIData[[]api.AdminSkillPackageResponse](t, f.server, "GET", path, nil)
			if len(result) != 1 || result[0].Icon != url {
				t.Fatalf("packages: %+v", result)
			}
		}
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0h24v24H0z"/></svg>`)
	if err := os.WriteFile(filepath.Join(root, "icon.svg"), svg, 0644); err != nil {
		t.Fatal(err)
	}
	rec := request(200, "image/svg+xml")
	if !bytes.Equal(rec.Body.Bytes(), svg) {
		t.Fatal("SVG should take precedence")
	}
	cached := httptest.NewRecorder()
	req := httptest.NewRequest("GET", url, nil)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	f.server.ServeHTTP(cached, req)
	if cached.Code != http.StatusNotModified {
		t.Fatalf("cache: %d", cached.Code)
	}
	if err := os.WriteFile(filepath.Join(root, "icon.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 0644); err != nil {
		t.Fatal(err)
	}
	request(404, "")
	if err := os.Remove(filepath.Join(root, "icon.svg")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(request(200, "image/png").Body.Bytes(), png) {
		t.Fatal("PNG fallback changed")
	}
	if err := os.Remove(filepath.Join(root, "icon.png")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "icon.png")
	if err := os.WriteFile(outside, png, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "icon.png")); err != nil {
		t.Fatal(err)
	}
	request(404, "")
	for _, key := range []string{"..%2Foffice", "office%2Fdoc"} {
		url = "/api/skill-packages/icon?key=" + key
		request(400, "")
	}
	url = "/api/skill-packages/icon?key=missing"
	request(404, "")
}
