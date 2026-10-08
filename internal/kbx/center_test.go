package kbx

import (
	"agent-platform/internal/builtins"
	"agent-platform/internal/kbasescenter"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCenterRetrievalMethodsAndSources(t *testing.T) {
	for _, method := range []string{"query", "search", "vsearch", "gsearch"} {
		t.Run(method, func(t *testing.T) {
			e := NewCenterEngine()
			e.runner = runFunc(func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
				if args[0] != method || !strings.Contains(strings.Join(args, " "), "-c docs -c reports") || args[len(args)-1] != "--fixture" {
					t.Fatalf("argv: %v", args)
				}
				if method == "gsearch" && strings.Contains(strings.Join(args, " "), "--full") {
					t.Fatal("unsupported graph flag")
				}
				rows := []any{map[string]any{"file": "kbx://reports/2026/manual.md", "resultId": "hit", "chunk": map[string]any{"seq": 1}, "evidence": map[string]any{"text": "fixture"}}}
				if method == "gsearch" {
					return responseJSON(rows), nil
				}
				return responseJSON(map[string]any{"results": rows, "trace": map[string]any{"degraded": false}}), nil
			})
			raw, err := e.Read(context.Background(), "fixture.sqlite", method, "--fixture", 10, "docs", "reports")
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Results []struct {
					Collection, RelativePath string
					Chunk                    struct{ Seq int }
					Evidence                 struct{ Text string }
				}
			}
			if err = json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Results) != 1 || result.Results[0].Collection != "reports" || result.Results[0].RelativePath != "2026/manual.md" || result.Results[0].Chunk.Seq != 1 || result.Results[0].Evidence.Text != "fixture" {
				t.Fatalf("source data lost: %s", raw)
			}
		})
	}
}

func TestCenterPartialRegistrationRetry(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.sqlite")
	if err := os.WriteFile(db, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	docs, _ := filepath.EvalSymlinks(t.TempDir())
	reports, _ := filepath.EvalSymlinks(t.TempDir())
	collections := []kbasescenter.Collection{{Name: "docs", SourcePath: docs}, {Name: "reports", SourcePath: reports}}
	var calls [][]string
	e := NewCenterEngine()
	e.runner = runFunc(func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "ls" {
			return responseJSON(map[string]any{"collections": []any{map[string]any{"name": "docs"}}}), nil
		}
		return []byte("status=complete"), nil
	})
	if err := e.Update(context.Background(), db, collections); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 || !reflect.DeepEqual(calls[1], []string{"collection", "set-path", "docs", docs}) || !reflect.DeepEqual(calls[2], []string{"update", "-c", "docs", "--no-commands"}) || !reflect.DeepEqual(calls[3], []string{"collection", "add", reports, "--name", "reports"}) {
		t.Fatalf("retry re-registered existing collection: %v", calls)
	}
}

func TestCenterCollectionRemovalFailure(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.sqlite")
	if err := os.WriteFile(db, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	source, _ := filepath.EvalSymlinks(t.TempDir())
	e := NewCenterEngine()
	var calls [][]string
	e.runner = runFunc(func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "ls" {
			return responseJSON(map[string]any{"collections": []any{map[string]any{"name": "removed"}}}), nil
		}
		return nil, fmt.Errorf("removal failed")
	})
	if err := e.Update(context.Background(), db, []kbasescenter.Collection{{Name: "docs", SourcePath: source}}); err == nil || !strings.Contains(err.Error(), "removal failed") {
		t.Fatal("removal failure ignored", err)
	}
	if len(calls) != 2 || !reflect.DeepEqual(calls[1], []string{"collection", "remove", "removed"}) {
		t.Fatalf("continued after failed removal: %v", calls)
	}
}

func TestCenterRealCollectionChanges(t *testing.T) {
	bin := os.Getenv("KBX_CENTER_TEST_BIN")
	if bin == "" {
		t.Skip("set KBX_CENTER_TEST_BIN to managed bin directory")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	var sources []string
	for _, name := range []string{"original", "removed", "rebound", "added"} {
		source := filepath.Join(root, name)
		if err := os.MkdirAll(source, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, name+".md"), []byte("# "+name+"\nquartzorchid mutation fixture."), 0600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, source)
	}
	db := filepath.Join(root, "library", "index.sqlite")
	if err := os.MkdirAll(filepath.Dir(db), 0700); err != nil {
		t.Fatal(err)
	}
	e := NewCenterEngine()
	if err := e.Update(context.Background(), db, []kbasescenter.Collection{{Name: "docs", SourcePath: sources[0]}, {Name: "reports", SourcePath: sources[1]}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Update(context.Background(), db, []kbasescenter.Collection{{Name: "docs", SourcePath: sources[2]}, {Name: "notes", SourcePath: sources[3]}}); err != nil {
		t.Fatal(err)
	}
	raw, err := e.Read(context.Background(), db, "files", "", 0, "docs", "notes")
	if err != nil {
		t.Fatal(err)
	}
	var files struct{ Documents []struct{ File string } }
	if err := json.Unmarshal(raw, &files); err != nil {
		t.Fatal(err)
	}
	if len(files.Documents) != 2 {
		t.Fatalf("files: %s", raw)
	}
	for _, doc := range files.Documents {
		if doc.File != "kbx://docs/rebound.md" && doc.File != "kbx://notes/added.md" {
			t.Fatalf("old document retained: %s", raw)
		}
	}
	for _, source := range sources {
		if _, err := os.Stat(source); err != nil {
			t.Fatal("source removed", err)
		}
	}
	raw, err = e.runner.Run(context.Background(), db, centerConfig, "ls", "--agent")
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct{ Collections []struct{ Name string } }
	if err := decodeEnvelope(raw, &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Collections) != 2 {
		t.Fatalf("stale collections: %s", raw)
	}
	for _, c := range inventory.Collections {
		if c.Name == "reports" {
			t.Fatalf("removed collection retained: %s", raw)
		}
	}
}

func TestCenterRealMultipleCollections(t *testing.T) {
	bin := os.Getenv("KBX_CENTER_TEST_BIN")
	if bin == "" {
		t.Skip("set KBX_CENTER_TEST_BIN to managed bin directory")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	collections := []kbasescenter.Collection{}
	for _, name := range []string{"docs", "reports"} {
		source := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(source, "2026"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "2026", "manual.md"), []byte("# "+name+"\nquartzorchid collection fixture."), 0600); err != nil {
			t.Fatal(err)
		}
		collections = append(collections, kbasescenter.Collection{Name: name, SourcePath: source})
	}
	db := filepath.Join(root, "library", "index.sqlite")
	if err := os.MkdirAll(filepath.Dir(db), 0700); err != nil {
		t.Fatal(err)
	}
	e := NewCenterEngine()
	for i := 0; i < 2; i++ {
		if err := e.Update(context.Background(), db, collections); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{"search", "query"} {
		raw, err := e.Read(context.Background(), db, method, "quartzorchid", 10, "docs", "reports")
		if err != nil {
			t.Fatal(err)
		}
		var data struct {
			Results []struct {
				File, Collection, RelativePath string
				Chunk                          struct{ ID string }
			}
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Results) != 2 {
			t.Fatalf("missing cross-collection results: %s", raw)
		}
		for _, hit := range data.Results {
			if hit.Collection == "" || hit.RelativePath != "2026/manual.md" || hit.Chunk.ID == "" {
				t.Fatalf("missing chunk source: %+v", hit)
			}
			if _, err := e.Read(context.Background(), db, "read", hit.File, 0); err != nil {
				t.Fatal(err)
			}
		}
		raw, err = e.Read(context.Background(), db, method, "quartzorchid", 10, "reports")
		if err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(raw, &data)
		if len(data.Results) != 1 || data.Results[0].Collection != "reports" {
			t.Fatalf("scope ignored: %s", raw)
		}
	}
	raw, err := e.Read(context.Background(), db, "files", "", 0, "docs", "reports")
	if err != nil {
		t.Fatal(err)
	}
	var files struct {
		Documents []struct{ Collection, RelativePath string }
	}
	if err := json.Unmarshal(raw, &files); err != nil || len(files.Documents) != 2 {
		t.Fatalf("files: %s %v", raw, err)
	}
	for _, method := range []string{"vsearch", "gsearch"} {
		if _, err := e.Read(context.Background(), db, method, "fixture", 10, "docs", "reports"); err == nil {
			t.Fatalf("unavailable %s returned success", method)
		}
	}
	// Exercise the configured vector path with artificial documents and a local provider.
	t.Setenv("NO_PROXY", "127.0.0.1,localhost,::1")
	t.Setenv("no_proxy", "127.0.0.1,localhost,::1")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid input", 400)
			return
		}
		var inputs []string
		if err := json.Unmarshal(request.Input, &inputs); err != nil {
			var single string
			json.Unmarshal(request.Input, &single)
			inputs = []string{single}
		}
		vectors := []any{}
		for i := range inputs {
			vectors = append(vectors, map[string]any{"index": i, "embedding": []float64{1, 0, 0, 0, 0, 0, 0, 0}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": vectors})
	}))
	defer provider.Close()
	m, l := newTestManager(t)
	m.models = fixtureModels{provider.URL}
	m.options.DefaultEmbeddingModelKey = "fixture"
	config, err := m.config(l, true)
	if err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(root, "models.yml")
	if err := os.WriteFile(configFile, config, 0600); err != nil {
		t.Fatal(err)
	}
	e.runner, e.embedding = cliRunner{configFile: configFile}, true
	if err := e.Update(context.Background(), db, collections); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"vsearch", "query"} {
		raw, err := e.Read(context.Background(), db, method, "unseenlexicaltoken", 10, "reports")
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Results []struct{ Collection, RelativePath string }
			Trace   struct {
				Coverage struct{ RetrievalUsed []string }
			}
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Results) != 1 || result.Results[0].Collection != "reports" || result.Results[0].RelativePath != "2026/manual.md" || !strings.Contains(strings.Join(result.Trace.Coverage.RetrievalUsed, ","), "vector") {
			t.Fatalf("vector scope or channel lost: %s", raw)
		}
	}
}

func TestCenterRealCLI(t *testing.T) {
	bin := os.Getenv("KBX_CENTER_TEST_BIN")
	if bin == "" {
		t.Skip("set KBX_CENTER_TEST_BIN to managed bin directory")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	e := NewCenterEngine()
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	source, _ = filepath.EvalSymlinks(source)
	doc := filepath.Join(source, "note.txt")
	os.WriteFile(doc, []byte("The zebra retrieval fixture."), 0600)
	db := filepath.Join(root, "library", "index.sqlite")
	os.Mkdir(filepath.Dir(db), 0700)
	collections := []kbasescenter.Collection{{Name: "workspace", SourcePath: source}}
	for i := 0; i < 2; i++ {
		if err := e.Update(ctx, db, collections); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}
	raw, err := e.Read(ctx, db, "search", "zebra", 5)
	if err != nil {
		t.Fatal(err)
	}
	var found searchResponse
	if err = json.Unmarshal(raw, &found); err != nil || len(found.Results) != 1 {
		t.Fatalf("search %s %v", raw, err)
	}
	raw, err = e.Read(ctx, db, "read", found.Results[0].File, 0)
	if err != nil {
		t.Fatal(err)
	}
	var read struct{ Body string }
	json.Unmarshal(raw, &read)
	if read.Body != "The zebra retrieval fixture." {
		t.Fatalf("read %s", raw)
	}
	os.WriteFile(doc, []byte("An elephant replaces the earlier document."), 0600)
	if err = e.Update(ctx, db, collections); err != nil {
		t.Fatal(err)
	}
	raw, err = e.Read(ctx, db, "search", "elephant", 5)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &found)
	if len(found.Results) != 1 {
		t.Fatalf("updated document absent: %s", raw)
	}
	os.Remove(doc)
	if err = e.Update(ctx, db, collections); err != nil {
		t.Fatal(err)
	}
	raw, err = e.Read(ctx, db, "files", "", 0, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct{ Documents []any }
	json.Unmarshal(raw, &inventory)
	if len(inventory.Documents) != 0 {
		t.Fatalf("deleted source still indexed: %s", raw)
	}
}
