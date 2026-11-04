package server

import (
	"agent-platform/internal/authn"

	"archive/zip"
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/webapp"
)

func webAppAdminServer(t *testing.T) *Server {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Linux Compose storage")
	}
	root := t.TempDir()
	service, err := webapp.Open(webapp.Options{Root: filepath.Join(root, "webapps"), DataRoot: filepath.Join(root, "webapp-data"), Config: config.DefaultWebAppsConfig()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Close()
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				return os.Chmod(p, 0700)
			}
			return err
		})
	})
	s := webAppBoundaryServer()
	s.deps.WebApps = service
	return s
}
func adminCall(s *Server, method, path, body, subject, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://demo.platform.example.com/api/admin/webapps"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer owner-token")
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r = r.WithContext(WithPrincipal(r.Context(), &authn.Principal{Subject: subject}))
	w := httptest.NewRecorder()
	s.handleWebAppAdmin(w, r)
	return w
}
func TestWebAppManagementRequiresOwnerAndExactOrigin(t *testing.T) {
	s := webAppAdminServer(t)
	for _, subject := range []string{"", "other-owner"} {
		if w := adminCall(s, "GET", "", "", subject, ""); w.Code != 403 {
			t.Fatalf("owner bypass %d", w.Code)
		}
	}
	for _, origin := range []string{"https://evil.example", "https://demo--0123456789abcdef.apps.example.net"} {
		if w := adminCall(s, "POST", "/install", "{}", "owner", origin); w.Code != 403 {
			t.Fatalf("origin bypass %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", "https://demo.platform.example.com/api/admin/webapps/install", strings.NewReader("{}"))
	r = r.WithContext(WithPrincipal(r.Context(), &authn.Principal{Subject: "owner"}))
	w := httptest.NewRecorder()
	s.handleWebAppAdmin(w, r)
	if w.Code != 403 {
		t.Fatal("cookie write without Origin was accepted")
	}
}
func TestWebAppManagementUploadReviewInstallAndDelete(t *testing.T) {
	s := webAppAdminServer(t)
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, data := range map[string]string{"webapp-0123456789abcdef/webapp.json": `{"schemaVersion":2,"id":"webapp-0123456789abcdef","key":"test-app","label":"Test app","version":"1.0.0","target":"linux-x64","frontend":{"root":"frontend","index":"index.html"}}`, "webapp-0123456789abcdef/frontend/index.html": "hello"} {
		f, _ := z.Create(name)
		_, _ = f.Write([]byte(data))
	}
	z.Close()
	r := httptest.NewRequest("POST", "https://demo.platform.example.com/api/admin/webapps/uploads", &b)
	r.Header.Set("Content-Type", "application/zip")
	r.Header.Set("Authorization", "Bearer owner-token")
	r = r.WithContext(WithPrincipal(r.Context(), &authn.Principal{Subject: "owner"}))
	w := httptest.NewRecorder()
	s.handleWebAppAdmin(w, r)
	if w.Code != 200 {
		t.Fatalf("upload %d %s", w.Code, w.Body.String())
	}
	var uploaded struct {
		Data webapp.Upload `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	w = adminCall(s, "POST", "/preflight", `{"uploadId":"`+uploaded.Data.UploadID+`"}`, "owner", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	input, _ := json.Marshal(webapp.InstallRequest{UploadID: uploaded.Data.UploadID, SHA256: uploaded.Data.SHA256})
	w = adminCall(s, "POST", "/install", string(input), "owner", "")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var accepted struct {
		Data webapp.Operation `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &accepted)
	var installed struct {
		Data webapp.Entry `json:"data"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		op, err := s.deps.WebApps.Operation(accepted.Data.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if op.State == "failed" {
			t.Fatal(op.ErrorCode)
		}
		if op.State == "succeeded" {
			installed.Data = *op.Result
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if installed.Data.ID == "" {
		t.Fatal("operation did not complete")
	}
	w = adminCall(s, "GET", "", "", "owner", "")
	if !strings.Contains(w.Body.String(), `"desiredState":"stopped"`) || strings.Contains(w.Body.String(), "installationId") || strings.Contains(w.Body.String(), "ContentSHA") {
		t.Fatal("invalid public projection", w.Body.String())
	}
	w = adminCall(s, "DELETE", "/"+installed.Data.ID, `{"expectedRevision":"`+installed.Data.Revision+`","purgeData":false}`, "owner", "")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = adminCall(s, "GET", "/"+installed.Data.ID, "", "owner", "")
	if w.Code != 404 {
		t.Fatal("deleted entry remained")
	}
}
func TestWebAppRequestsRejectUnknownCasingDuplicatesAndTrailingJSON(t *testing.T) {
	s := webAppAdminServer(t)
	for _, body := range []string{`{"UploadID":"x"}`, `{"uploadId":null}`, `{"uploadId":"x","archivePath":"/etc/passwd"}`, `{"uploadId":"x","uploadId":"y"}`, `{"uploadId":"x"} {}`} {
		w := adminCall(s, http.MethodPost, "/preflight", body, "owner", "")
		if w.Code != 400 || !strings.Contains(w.Body.String(), "webapp_invalid_request") {
			t.Fatalf("invalid request accepted: %s %s", body, w.Body.String())
		}
	}
}
