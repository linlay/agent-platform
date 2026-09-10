package documentpreview

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeHub struct {
	server                   *httptest.Server
	uploads, shares, deletes atomic.Int32
	exists                   atomic.Bool
	failUpload               atomic.Bool
	wrongOrigin              atomic.Bool
	activeShare              atomic.Bool
	failDelete               atomic.Bool
	token                    atomic.Value
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	h := &fakeHub{}
	h.exists.Store(true)
	h.token.Store("")
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token := h.token.Load().(string); token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/documents/upload":
			h.uploads.Add(1)
			if h.failUpload.Load() {
				w.WriteHeader(500)
				return
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			f, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			_, _ = io.Copy(io.Discard, f)
			f.Close()
			h.exists.Store(true)
			w.WriteHeader(201)
			fmt.Fprint(w, `{"id":"11111111-1111-4111-8111-111111111111"}`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/share-links"):
			h.shares.Add(1)
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["expiry"] != "1d" || payload["allowDownload"] != false || payload["allowPrint"] != false || payload["allowCopy"] != true {
				t.Errorf("bad share policy: %+v", payload)
			}
			origin := h.server.URL
			if h.wrongOrigin.Load() {
				origin = "https://untrusted.test"
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"url": origin + "/s/opaque-token", "link": map[string]any{"id": "22222222-2222-4222-8222-222222222222", "expiresAt": time.Now().Add(24 * time.Hour)}})
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/versions"):
			if !h.exists.Load() {
				w.WriteHeader(404)
				return
			}
			fmt.Fprint(w, `{"items":[]}`)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/share-links"):
			if h.activeShare.Load() {
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"expiresAt": time.Now().Add(time.Hour)}}})
			} else {
				fmt.Fprint(w, `{"items":[]}`)
			}
		case r.Method == "DELETE":
			h.deletes.Add(1)
			if h.failDelete.Load() {
				w.WriteHeader(500)
				return
			}
			h.exists.Store(false)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected hub request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(h.server.Close)
	return h
}

func officeFile(t *testing.T, path, content string) {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	main := map[string]string{".docx": "word/document.xml", ".pptx": "ppt/presentation.xml", ".xlsx": "xl/workbook.xml"}[filepath.Ext(path)]
	for name, value := range map[string]string{"[Content_Types].xml": "<Types/>", main: content} {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(value))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (*Service, *fakeHub, Resolver) {
	t.Helper()
	h := newFakeHub(t)
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.APIBaseURL = h.server.URL
	cfg.PublicBaseURL = h.server.URL
	s, err := New(cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.docx")
	officeFile(t, path, "first")
	return s, h, func() (Resolved, error) { return Resolved{Path: path, Identity: "workspace:agent:" + path}, nil }
}

func prepare(t *testing.T, s *Service, id, user string, resolve Resolver) Result {
	t.Helper()
	result, err := s.Prepare(context.Background(), user, Request{RequestID: id}, resolve)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCacheConcurrentRequestsAndRevision(t *testing.T) {
	s, h, resolve := fixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Prepare(context.Background(), "u1", Request{RequestID: fmt.Sprint(i)}, resolve)
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if h.uploads.Load() != 1 || h.shares.Load() != 1 {
		t.Fatalf("uploads=%d shares=%d", h.uploads.Load(), h.shares.Load())
	}
	prepare(t, s, "other-user", "u2", resolve)
	if h.uploads.Load() != 2 {
		t.Fatal("cache crossed users")
	}
	file, _ := resolve()
	officeFile(t, file.Path, "changed content")
	prepare(t, s, "changed", "u1", resolve)
	if h.uploads.Load() != 3 {
		t.Fatal("content change reused old copy")
	}
	_, err := s.Prepare(context.Background(), "u1", Request{RequestID: "0"}, resolve)
	var e *Error
	if !errors.As(err, &e) || e.Code != "request_id_conflict" {
		t.Fatalf("request binding err=%v", err)
	}
}

func TestEveryRequestRechecksAccessAndSurvivesRestart(t *testing.T) {
	s, h, resolve := fixture(t)
	r := prepare(t, s, "same", "user", resolve)
	_, err := s.Prepare(context.Background(), "user", Request{RequestID: "same"}, func() (Resolved, error) { return Resolved{}, errors.New("denied") })
	if err == nil || h.uploads.Load() != 1 {
		t.Fatal("cached result bypassed access")
	}
	restarted, err := New(s.config, filepath.Dir(s.dir))
	if err != nil {
		t.Fatal(err)
	}
	next := prepare(t, restarted, "same", "user", resolve)
	if next.PreviewID != r.PreviewID || h.uploads.Load() != 1 {
		t.Fatal("restart lost successful upload")
	}
}

func TestExpiredLinksRenewAndMissingCopiesUploadAgain(t *testing.T) {
	s, h, resolve := fixture(t)
	r := prepare(t, s, "a", "u", resolve)
	var saved record
	_ = s.read(r.PreviewID+".json", &saved)
	saved.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
	_ = s.write(r.PreviewID+".json", saved)
	prepare(t, s, "b", "u", resolve)
	if h.uploads.Load() != 1 || h.shares.Load() != 2 {
		t.Fatal("renewal uploaded another copy")
	}
	h.exists.Store(false)
	prepare(t, s, "c", "u", resolve)
	if h.uploads.Load() != 2 {
		t.Fatal("missing remote copy was reused")
	}
}

func TestCredentialsOriginsAndUnknownUpload(t *testing.T) {
	s, h, resolve := fixture(t)
	h.failUpload.Store(true)
	for i := 0; i < 2; i++ {
		_, err := s.Prepare(context.Background(), "u", Request{RequestID: "unknown"}, resolve)
		if err == nil {
			t.Fatal("expected upload failure")
		}
	}
	if h.uploads.Load() != 1 {
		t.Fatal("unknown upload retried")
	}
	h.failUpload.Store(false)
	h.wrongOrigin.Store(true)
	_, err := s.Prepare(context.Background(), "u", Request{RequestID: "explicit-retry"}, resolve)
	var e *Error
	if !errors.As(err, &e) || e.Code != "preview_invalid_url" {
		t.Fatalf("unsafe URL accepted: %v", err)
	}
	h.wrongOrigin.Store(false)
	prepare(t, s, "another", "u", resolve)
	if h.uploads.Load() != 2 {
		t.Fatal("known document ID was lost after sharing failed")
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(tokenFile, []byte("secret-one"), 0600)
	s.config.AuthMode = "bearer-token-file"
	s.config.TokenFile = tokenFile
	h.token.Store("secret-one")
	prepare(t, s, "auth", "u", resolve)
	if h.uploads.Load() != 3 {
		t.Fatal("auth configuration did not isolate cache")
	}
	_ = os.WriteFile(tokenFile, []byte("secret-two"), 0600)
	h.token.Store("secret-two")
	prepare(t, s, "auth", "u", resolve)
	if h.uploads.Load() != 4 {
		t.Fatal("rotated credential not loaded")
	}
}

func TestCleanupOnlyDeletesOwnedExpiredCopiesInCurrentService(t *testing.T) {
	s, h, resolve := fixture(t)
	r := prepare(t, s, "a", "u", resolve)
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.deletes.Load() != 0 {
		t.Fatal("live preview was deleted")
	}
	var saved record
	_ = s.read(r.PreviewID+".json", &saved)
	saved.LastAccess = time.Now().Add(-25 * time.Hour).UnixMilli()
	saved.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
	_ = s.write(r.PreviewID+".json", saved)
	original := s.config.PublicBaseURL
	s.config.PublicBaseURL = "https://another.test"
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.deletes.Load() != 0 {
		t.Fatal("another service's copy deleted")
	}
	s.config.PublicBaseURL = original
	h.activeShare.Store(true)
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.deletes.Load() != 0 {
		t.Fatal("remote active share was ignored during recycling")
	}
	h.activeShare.Store(false)
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.deletes.Load() != 1 {
		t.Fatal("expired owned copy was not deleted")
	}
}

func TestFormatAndSizeValidation(t *testing.T) {
	for _, ext := range []string{".docx", ".pptx", ".xlsx"} {
		t.Run(ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file"+ext)
			officeFile(t, path, "contents")
			if _, err := readSnapshot(Resolved{Path: path}, 1024); err != nil {
				t.Fatal(err)
			}
		})
	}
	s, h, resolve := fixture(t)
	s.config.MaxFileBytes = 1
	if _, err := s.Prepare(context.Background(), "u", Request{RequestID: "size"}, resolve); err == nil {
		t.Fatal("oversized file accepted")
	}
	s.config.MaxFileBytes = 1000
	file, _ := resolve()
	_ = os.WriteFile(file.Path, []byte("not OOXML"), 0600)
	if _, err := s.Prepare(context.Background(), "u", Request{RequestID: "invalid"}, resolve); err == nil {
		t.Fatal("invalid document accepted")
	}
	if h.uploads.Load() != 0 {
		t.Fatal("invalid file reached upload")
	}
}

func TestRedirectNeverReceivesCredentialsOrUpload(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("followed upload redirect") }))
	defer destination.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	cfg := DefaultConfig()
	cfg.APIBaseURL = upstream.URL
	h, err := newHub(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.request(context.Background(), "POST", "/upload", "text/plain", strings.NewReader("file bytes"), nil); err == nil {
		t.Fatal("redirect accepted")
	}
}

func TestConcurrentUnknownUploadDoesNotRetry(t *testing.T) {
	s, _, resolve := fixture(t)
	entered, finish := make(chan struct{}), make(chan struct{})
	var uploads atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads.Add(1)
		if uploads.Load() == 1 {
			close(entered)
			<-finish
		}
		w.WriteHeader(500)
	}))
	defer remote.Close()
	s.config.APIBaseURL, s.config.PublicBaseURL = remote.URL, remote.URL
	results := make(chan error, 2)
	go func() {
		_, err := s.Prepare(context.Background(), "user", Request{RequestID: "first"}, resolve)
		results <- err
	}()
	<-entered
	secondStarted := make(chan struct{})
	go func() {
		_, err := s.Prepare(context.Background(), "user", Request{RequestID: "second"}, func() (Resolved, error) { close(secondStarted); return resolve() })
		results <- err
	}()
	<-secondStarted
	close(finish)
	for i := 0; i < 2; i++ {
		var e *Error
		if err := <-results; !errors.As(err, &e) || e.Code != "preview_upload_outcome_unknown" {
			t.Fatalf("result=%v", err)
		}
	}
	if uploads.Load() != 1 {
		t.Fatalf("concurrent requests retried upload %d times", uploads.Load())
	}
	_, _ = s.Prepare(context.Background(), "user", Request{RequestID: "explicit-retry"}, resolve)
	if uploads.Load() != 2 {
		t.Fatal("new explicit request did not retry")
	}
}

func TestPreviewTimeoutDoesNotRepeatUnknownUpload(t *testing.T) {
	s, _, resolve := fixture(t)
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer remote.Close()
	s.config.APIBaseURL, s.config.PublicBaseURL = remote.URL, remote.URL
	s.config.RequestTimeout = 100 * time.Millisecond
	for i := 0; i < 2; i++ {
		_, err := s.Prepare(context.Background(), "u", Request{RequestID: "timeout"}, resolve)
		var e *Error
		if !errors.As(err, &e) || e.Code != "preview_upload_outcome_unknown" {
			t.Fatalf("timeout result=%v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("timed out upload retried automatically")
	}
	s.slots <- struct{}{}
	s.slots <- struct{}{}
	_, err := s.Prepare(context.Background(), "u", Request{RequestID: "queued"}, resolve)
	var e *Error
	if !errors.As(err, &e) || e.Code != "preview_timeout" {
		t.Fatalf("queued timeout=%v", err)
	}
}

func TestPreviewCleanupFailureRetainsManagedRecord(t *testing.T) {
	s, h, resolve := fixture(t)
	result := prepare(t, s, "created", "u", resolve)
	var saved record
	if err := s.read(result.PreviewID+".json", &saved); err != nil {
		t.Fatal(err)
	}
	saved.LastAccess = time.Now().Add(-25 * time.Hour).UnixMilli()
	saved.ExpiresAt = 0
	if err := s.write(result.PreviewID+".json", saved); err != nil {
		t.Fatal(err)
	}
	h.failDelete.Store(true)
	if err := s.Cleanup(context.Background()); err == nil {
		t.Fatal("expected cleanup failure")
	}
	if err := s.read(result.PreviewID+".json", &saved); err != nil {
		t.Fatal("failed cleanup lost managed record")
	}
	h.failDelete.Store(false)
	if err := s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.deletes.Load() != 2 {
		t.Fatal("cleanup did not retry")
	}
}
