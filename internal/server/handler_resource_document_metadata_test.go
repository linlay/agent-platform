package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestResourceHeadReturnsAuthoritativeDocumentMetadata(t *testing.T) {
	fixture := newTestFixture(t)
	const chatID = "chat-resource-head"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "resource"); err != nil {
		t.Fatal(err)
	}
	relativePath := "artifacts/run-head/page.html"
	targetPath := filepath.Join(fixture.chats.ChatDir(chatID), filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("<!doctype html><title>Head</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	resourceURL := "/api/resource?file=" + url.QueryEscape(chatID+"/"+relativePath)
	recorder := httptest.NewRecorder()
	fixture.server.ServeHTTP(recorder, httptest.NewRequest(http.MethodHead, resourceURL, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("HEAD status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("HEAD returned %d body bytes", recorder.Body.Len())
	}
	if got := recorder.Header().Get("X-ZenMind-Document-Kind"); got != "document-html" {
		t.Fatalf("document kind=%q", got)
	}
	if got := recorder.Header().Get("X-ZenMind-Resource-Revision"); got == "" {
		t.Fatal("missing resource revision")
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("content type=%q", got)
	}
	if got := recorder.Header().Get("Content-Length"); got != "34" {
		t.Fatalf("content length=%q", got)
	}

	methodDenied := httptest.NewRecorder()
	fixture.server.ServeHTTP(methodDenied, httptest.NewRequest(http.MethodPost, resourceURL, nil))
	if methodDenied.Code != http.StatusMethodNotAllowed || methodDenied.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST status=%d allow=%q", methodDenied.Code, methodDenied.Header().Get("Allow"))
	}
}

func TestServeResourcePathClassifiesByCanonicalSemanticName(t *testing.T) {
	fixture := newTestFixture(t)
	physicalPath := filepath.Join(t.TempDir(), "cached-resource")
	body := []byte("# 招聘启事\n\n正文\n")
	if err := os.WriteFile(physicalPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, method := range []string{http.MethodHead, http.MethodGet} {
		recorder := httptest.NewRecorder()
		fixture.server.serveResourcePath(
			recorder,
			httptest.NewRequest(method, "/api/resource", nil),
			physicalPath,
			"artifacts/run-1/第十一层的招聘启事.md",
		)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", method, recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("X-ZenMind-Document-Kind"); got != documentKindMarkdown {
			t.Fatalf("%s document kind=%q", method, got)
		}
		if got := recorder.Header().Get("Content-Type"); got != "text/markdown; charset=utf-8" {
			t.Fatalf("%s content type=%q", method, got)
		}
		if got := recorder.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
			t.Fatalf("%s content length=%q", method, got)
		}
		if recorder.Header().Get("X-ZenMind-Resource-Revision") == "" {
			t.Fatalf("%s missing revision", method)
		}
		if method == http.MethodHead && recorder.Body.Len() != 0 {
			t.Fatalf("HEAD returned %d body bytes", recorder.Body.Len())
		}
		if method == http.MethodGet && !strings.Contains(recorder.Body.String(), "招聘启事") {
			t.Fatalf("GET body=%q", recorder.Body.String())
		}
	}
}
