package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/documentpreview"
)

// Opt-in local UI fixture: serve the actual WebClient build against Platform
// and a local document-hub. Runtime files remain isolated under t.TempDir.
func TestLiveDocumentPreviewSurface(t *testing.T) {
	dist, fixtures, ready := os.Getenv("DOCUMENT_PREVIEW_WEBCLIENT_DIST"), os.Getenv("DOCUMENT_PREVIEW_FIXTURES"), os.Getenv("DOCUMENT_PREVIEW_UI_READY")
	if dist == "" || fixtures == "" || ready == "" {
		t.Skip("set Document Preview local UI fixture paths to enable")
	}
	f, workspace, _ := newAgentFileTestFixture(t)
	for _, name := range []string{"preview-long.docx", "preview-slides.pptx", "preview-sheets.xlsx"} {
		data, err := os.ReadFile(filepath.Join(fixtures, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := documentpreview.DefaultConfig()
	cfg.Enabled = true
	service, err := documentpreview.New(cfg, os.Getenv("DOCUMENT_PREVIEW_TEST_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	f.server.documentPreview = service
	f.server.deps.Config.DocumentPreview = cfg
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			f.server.ServeHTTP(w, r)
		case r.URL.Path == "/runtime-config.js":
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprint(w, `globalThis.__AGENT_WEBCLIENT_RUNTIME_CONFIG__ = {DESKTOP_APP:"false"};`)
		case strings.HasPrefix(r.URL.Path, "/file-viewer/"):
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
		default:
			http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	if err := os.WriteFile(ready, []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("local Document Surface ready at %s/file-viewer/coder-file?path=preview-sheets.xlsx", server.URL)
	deadline := time.NewTimer(10 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(ready + ".done"); err == nil {
				return
			}
		case <-deadline.C:
			t.Fatal("UI fixture timed out; write the .done file after inspection")
		}
	}
}
