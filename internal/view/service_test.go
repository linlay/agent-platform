package view

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func localMount(t *testing.T, id, html string) Mount {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "views"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "views", "index.html"), []byte(html), 0644); err != nil {
		t.Fatal(err)
	}
	return Mount{ID: id, Version: "1.0.0", Dir: root, Views: map[string]Definition{"main": {Renderer: "html", Usage: []string{"display", "form"}, Entry: "views/index.html"}}}
}

func TestMountedViewsAreScopedAndSnapshotsSurviveUpdates(t *testing.T) {
	a, b := localMount(t, "a", "<p>original</p>"), localMount(t, "b", "<p>other</p>")
	s := &Service{}
	if _, err := s.Resolve(context.Background(), []Mount{a}, Reference{ConnectorID: "b", Key: "main"}, "display"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unmounted view: %v", err)
	}
	doc, err := s.Resolve(context.Background(), []Mount{a, b}, Reference{ConnectorID: "a", Key: "main"}, "form")
	if err != nil || doc.HTML != "<p>original</p>" {
		t.Fatalf("resolve: %#v %v", doc, err)
	}
	chatDir := t.TempDir()
	ref, err := SaveSnapshot(chatDir, doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Dir, "views/index.html"), []byte("<p>new</p>"), 0644); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Resolve(context.Background(), []Mount{a}, ref, "form")
	if err != nil {
		t.Fatal(err)
	}
	newRef, err := SaveSnapshot(chatDir, updated)
	if err != nil || newRef.Hash == ref.Hash {
		t.Fatalf("updated snapshot: %v", err)
	}
	original, err := LoadSnapshot(chatDir, ref)
	if err != nil || original.HTML != "<p>original</p>" {
		t.Fatalf("history changed: %#v %v", original, err)
	}
	if _, err := LoadSnapshot(t.TempDir(), ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-chat snapshot: %v", err)
	}
	wrong := ref
	wrong.ConnectorID = "b"
	if _, err := LoadSnapshot(chatDir, wrong); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong reference: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chatDir, SnapshotDirectory, ref.Hash+".json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(chatDir, ref); !errors.Is(err, ErrInvalid) {
		t.Fatal("tampered snapshot accepted")
	}
}

func TestViewsRejectUndeclaredPathsAndSymlinkEscapes(t *testing.T) {
	m := localMount(t, "a", "hello")
	for _, entry := range []string{"../secret", "views/../../secret", "views/../skills/secret", "skills/secret", "views/a\\b", "views/%2e%2e/secret"} {
		d := m.Views["main"]
		d.Entry = entry
		if err := ValidateDefinitions(m.Dir, map[string]Definition{"main": d}); err == nil {
			t.Fatalf("accepted %q", entry)
		}
	}
	if err := os.WriteFile(filepath.Join(m.Dir, "secret"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../secret", filepath.Join(m.Dir, "views", "escape.html")); err != nil {
		t.Skip(err)
	}
	if _, err := readResource(m.Dir, "views/escape.html"); err == nil {
		t.Fatal("view escaped its public subtree")
	}
	d := m.Views["main"]
	d.Usage = []string{"display"}
	m.Views["main"] = d
	if _, err := (&Service{}).Resolve(context.Background(), []Mount{m}, Reference{ConnectorID: "a", Key: "main"}, "form"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("display-only form: %v", err)
	}
}

func TestRemoteViewFetchOnlySendsTemplateKeyAndDoesNotFollowRedirects(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing server credential")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": "view", "result": map[string]any{"renderer": "html", "payload": "<form>safe</form>"}})
	}))
	defer server.Close()
	s := &Service{ResolveHeaders: func(_ string, _ map[string]string) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer test-token"}, nil
	}}
	m := Mount{ID: "remote", Version: "1.0.0", Views: map[string]Definition{"main": {Renderer: "html", Usage: []string{"form"}, Remote: &Remote{URL: server.URL, Key: "original"}}}}
	doc, err := s.Resolve(context.Background(), []Mount{m}, Reference{ConnectorID: "remote", Key: "main"}, "form")
	if err != nil || doc.HTML != "<form>safe</form>" {
		t.Fatalf("remote: %#v %v", doc, err)
	}
	if requests[0]["method"] != "views/get" || len(requests[0]["params"].(map[string]any)) != 1 {
		t.Fatalf("request contract: %#v", requests)
	}
	data, _ := json.Marshal(doc)
	if strings.Contains(string(data), "test-token") || strings.Contains(string(data), server.URL) {
		t.Fatal("private configuration exposed")
	}
	d := m.Views["main"]
	d.Remote.URL += "/redirect"
	m.Views["main"] = d
	if _, err := s.Resolve(context.Background(), []Mount{m}, Reference{ConnectorID: "remote", Key: "main"}, "form"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("redirect: %v", err)
	}
	if len(requests) != 2 {
		t.Fatal("followed redirect")
	}
}

func TestConcurrentSnapshotPublicationAndStalePendingFile(t *testing.T) {
	m := localMount(t, "a", "hello")
	doc, err := (&Service{}).Resolve(context.Background(), []Mount{m}, Reference{ConnectorID: "a", Key: "main"}, "display")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, err := SaveSnapshot(root, doc)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := LoadSnapshot(root, ref); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
