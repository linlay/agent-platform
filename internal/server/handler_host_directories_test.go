package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/api"
)

func TestHostDirectoriesListsOnlyDirectories(t *testing.T) {
	fixture := newTestFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"beta", "Alpha", ".hidden", "target"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("secret content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	list := func(query string) (int, api.HostDirectoryListResponse, string) {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/host/directories?"+query, nil))
		var response api.ApiResponse[api.HostDirectoryListResponse]
		_ = json.Unmarshal(rec.Body.Bytes(), &response)
		return rec.Code, response.Data, rec.Body.String()
	}
	names := func(entries []api.HostDirectoryEntry) []string {
		out := make([]string, 0, len(entries))
		for _, entry := range entries {
			out = append(out, entry.Name)
		}
		return out
	}

	code, data, body := list("path=" + url.QueryEscape(root))
	if code != http.StatusOK {
		t.Fatalf("list returned %d: %s", code, body)
	}
	if got := names(data.Entries); len(got) != 4 || got[0] != "Alpha" || got[1] != "beta" || got[2] != "link" || got[3] != "target" {
		t.Fatalf("entries = %v", got)
	}
	if data.Path != root || data.Parent != filepath.Dir(root) || data.Entries[0].Path != filepath.Join(root, "Alpha") || data.Separator == "" {
		t.Fatalf("unexpected listing: %#v", data)
	}
	if _, hidden, _ := list("showHidden=true&path=" + url.QueryEscape(root)); len(hidden.Entries) != 5 || hidden.Entries[0].Name != ".hidden" {
		t.Fatalf("hidden directories must be listed on request: %v", names(hidden.Entries))
	}

	// Without a path the listing starts at the Platform user's home.
	if code, data, body := list(""); code != http.StatusOK || data.Path == "" || data.Path != data.Home {
		t.Fatalf("default listing returned %d %#v: %s", code, data, body)
	}

	for query, want := range map[string]int{
		"path=relative/dir": http.StatusBadRequest,
		"path=" + url.QueryEscape(filepath.Join(root, "missing")):  http.StatusNotFound,
		"path=" + url.QueryEscape(filepath.Join(root, "notes.md")): http.StatusBadRequest,
		"showHidden=maybe": http.StatusBadRequest,
	} {
		if code, _, body := list(query); code != want {
			t.Fatalf("%s returned %d, want %d: %s", query, code, want, body)
		}
	}
}
