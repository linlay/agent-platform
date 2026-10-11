package kbases

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"agent-platform/internal/contracts"
)

func controlLibraryService(t *testing.T) *Service {
	t.Helper()
	s, err := New(context.Background(), t.TempDir(), t.TempDir(), testEngine{}, Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func createArguments(source string) map[string]any {
	return map[string]any{"definition": map[string]any{"name": "Documents", "description": "Preserve this", "collections": []any{map[string]any{"name": "docs", "sourcePath": source}}}}
}

func TestLibraryControlStatusRevisionPatchAndReplay(t *testing.T) {
	s := controlLibraryService(t)
	args := createArguments(t.TempDir())
	plan, err := s.PrepareControl("create", args, "create")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.SourcePermissions[0].Include) == 0 {
		t.Fatal("review omits effective include defaults")
	}
	if _, err := s.ExecuteControl("create", args, "create", func(string) bool { return false }); err == nil {
		t.Fatal("created without approval")
	}
	result, err := s.ExecuteControl("create", args, "create", func(digest string) bool { return digest == plan.Digest })
	if err != nil {
		t.Fatal(err)
	}
	id := result["libraryId"].(string)
	revision := result["baseRevision"].(string)
	status, err := s.LibraryStatus(context.Background(), id)
	if err != nil || status["state"] != "unindexed" || status["statsAvailable"] != false || status["statistics"] != nil {
		t.Fatalf("premature statistics: %#v %v", status, err)
	}
	replay, err := s.ExecuteControl("create", args, "create", nil)
	if err != nil || replay["libraryId"] != id {
		t.Fatalf("replay: %#v %v", replay, err)
	}
	if _, err := s.ExecuteControl("create", createArguments(t.TempDir()), "create", nil); err == nil || !strings.Contains(err.Error(), "invocation_conflict") {
		t.Fatal("changed request reused receipt")
	}
	if _, err := s.Refresh(id); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, id, "ready")
	d, _ := s.Get(id)
	if d.BaseRevision != revision {
		t.Fatal("indexing changed definition revision")
	}
	update := map[string]any{"libraryId": id, "baseRevision": revision, "patch": map[string]any{"name": "Renamed"}}
	plan, err = s.PrepareControl("update", update, "update")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.ExecuteControl("update", update, "update", func(digest string) bool { return digest == plan.Digest })
	if err != nil {
		t.Fatal(err)
	}
	d, _ = s.Get(id)
	if d.Name != "Renamed" || d.Description != "Preserve this" || updated["baseRevision"] == revision {
		t.Fatalf("partial patch lost fields: %+v", d)
	}
	if _, err := s.PrepareControl("update", update, "stale"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if err := s.DeleteRevision(id, revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	remove := map[string]any{"libraryId": id, "baseRevision": d.BaseRevision}
	plan, err = s.PrepareControl("delete", remove, "delete")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteControl("delete", remove, "delete", func(digest string) bool { return digest == plan.Digest }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteControl("delete", remove, "delete", nil); err != nil {
		t.Fatalf("delete replay required deleted object: %v", err)
	}
}

func TestLibraryControlConcurrentReuseAndDifferentScope(t *testing.T) {
	s := controlLibraryService(t)
	source := t.TempDir()
	ids := make(chan string, 12)
	errorsCh := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			result, err := s.ExecuteControl("create", createArguments(source), key, func(string) bool { return true })
			if err != nil {
				errorsCh <- err
				return
			}
			ids <- result["libraryId"].(string)
		}(string(rune('a' + i)))
	}
	wg.Wait()
	close(ids)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent create duplicated equivalent libraries")
		}
	}
	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("libraries: %d", len(list))
	}
	args := createArguments(source)
	args["definition"].(map[string]any)["collections"].([]any)[0].(map[string]any)["include"] = []string{"**/*.pdf"}
	plan, err := s.PrepareControl("create", args, "different-filter")
	if err != nil || plan.Reused {
		t.Fatalf("different scope silently reused: %#v %v", plan, err)
	}
}

func TestLibraryControlRechecksCanonicalSourcesAndProtectsIdentity(t *testing.T) {
	s := controlLibraryService(t)
	identityDir := t.TempDir()
	identity := filepath.Join(identityDir, "identity.txt")
	if err := os.WriteFile(identity, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	s.options.ProtectedPaths = []string{identity, s.options.StateDir}
	for _, source := range []string{identityDir, s.options.StateDir} {
		if _, err := s.PrepareControl("create", createArguments(source), ""); err == nil {
			t.Fatalf("protected source accepted: %s", source)
		}
	}
	link := filepath.Join(t.TempDir(), "source")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	args := createArguments(link)
	plan, err := s.PrepareControl("create", args, "redirect")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(identityDir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteControl("create", args, "redirect", func(digest string) bool { return digest == plan.Digest }); err == nil {
		t.Fatal("redirected source reused approval")
	}
	list, _ := s.List()
	if len(list) != 0 {
		t.Fatal("protected candidate was published")
	}
}

func TestLibraryControlReuseComparesNormalizedSourcesAndConfiguration(t *testing.T) {
	s := controlLibraryService(t)
	docs, reports := t.TempDir(), t.TempDir()
	arguments := func() map[string]any {
		return map[string]any{"definition": map[string]any{
			"name": "Library A", "description": "Display description",
			"collections": []any{
				map[string]any{"name": "docs", "sourcePath": docs, "include": []any{"**/*.md", "**/*.txt"}, "exclude": []any{"private/**", "build/**"}},
				map[string]any{"name": "reports", "sourcePath": reports},
			},
		}}
	}
	result, err := s.ExecuteControl("create", arguments(), "original", func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "documents alias")
	if err := os.Symlink(docs, alias); err != nil {
		t.Fatal(err)
	}
	equivalent := arguments()
	definition := equivalent["definition"].(map[string]any)
	definition["name"], definition["description"] = "Another name", "Another display description"
	collections := definition["collections"].([]any)
	collection := collections[0].(map[string]any)
	collection["sourcePath"] = alias
	collection["include"], collection["exclude"] = []any{"**/*.txt", "**/*.md"}, []any{"build/**", "private/**"}
	definition["collections"] = []any{collections[1], collections[0]}
	reused, err := s.ExecuteControl("create", equivalent, "equivalent", nil)
	if err != nil || reused["reused"] != true || reused["libraryId"] != result["libraryId"] {
		t.Fatalf("normalized equivalent configuration was not reused: %+v %v", reused, err)
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any, map[string]any)
	}{
		{"source", func(_ map[string]any, c map[string]any) { c["sourcePath"] = t.TempDir() }},
		{"collection-name", func(_ map[string]any, c map[string]any) { c["name"] = "other-docs" }},
		{"include", func(_ map[string]any, c map[string]any) { c["include"] = []any{"**/*.pdf"} }},
		{"exclude", func(_ map[string]any, c map[string]any) { c["exclude"] = []any{} }},
		{"collection-description", func(_ map[string]any, c map[string]any) { c["description"] = "Different editing scope description" }},
		{"default-query", func(_ map[string]any, c map[string]any) { c["defaultQuery"] = false }},
		{"chunk", func(_ map[string]any, c map[string]any) { c["chunk"] = map[string]any{"maxChars": 3000} }},
		{"encoding", func(d map[string]any, _ map[string]any) { d["textEncoding"] = "gb18030" }},
		{"retrieval", func(d map[string]any, _ map[string]any) { d["retrieval"] = map[string]any{"topK": 12} }},
		{"model", func(d map[string]any, _ map[string]any) {
			d["models"] = map[string]any{"embedding": map[string]any{"modelKey": "another-embedding-model"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := arguments()
			d := args["definition"].(map[string]any)
			tc.change(d, d["collections"].([]any)[0].(map[string]any))
			plan, err := s.PrepareControl("create", args, "")
			if err != nil || plan.Reused {
				t.Fatalf("different %s reused the existing library: %+v %v", tc.name, plan, err)
			}
		})
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].Name != "Library A" {
		t.Fatalf("validation/reuse unexpectedly changed persisted definitions: %+v %v", list, err)
	}
}

func TestLibraryControlUnknownReceiptAndInvalidBinding(t *testing.T) {
	s := controlLibraryService(t)
	args := createArguments(t.TempDir())
	plan, err := s.PrepareControl("create", args, "interrupted")
	if err != nil {
		t.Fatal(err)
	}
	path, _ := s.receiptPath("interrupted")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(controlReceipt{Plan: *plan, State: "started"})
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExecuteControl("create", args, "interrupted", func(string) bool { return true }); err == nil {
		t.Fatal("interrupted mutation replayed automatically")
	}
	d, err := s.Create(Input{Name: "Broken", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, d.ID, "library.yml"), []byte("unknown: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	diagnostic, err := s.Get(d.ID)
	if err != nil || diagnostic.Error == "" || diagnostic.BaseRevision == "" {
		t.Fatalf("diagnostic baseline missing: %+v %v", diagnostic, err)
	}
	if err := s.ValidateBinding(d.ID); err == nil {
		t.Fatal("diagnostic object counted as bindable library")
	}
}

func TestLibraryControlCanonicalScopeRecheckedDuringPublication(t *testing.T) {
	for _, action := range []string{"create", "update"} {
		t.Run(action, func(t *testing.T) {
			s := controlLibraryService(t)
			source := filepath.Join(t.TempDir(), "source")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			args := createArguments(source)
			var configPath string
			var before []byte
			if action == "update" {
				d, err := s.Create(Input{Name: "Original", SourcePath: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				configPath = filepath.Join(s.root, d.ID, "library.yml")
				before, err = os.ReadFile(configPath)
				if err != nil {
					t.Fatal(err)
				}
				args = map[string]any{"libraryId": d.ID, "baseRevision": d.BaseRevision, "patch": map[string]any{"collections": args["definition"].(map[string]any)["collections"]}}
			}
			_, err := s.ExecuteControl(action, args, "late-redirect", func(string) bool {
				if err := os.Rename(source, source+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), source); err != nil {
					t.Fatal(err)
				}
				return true
			})
			if !errors.Is(err, ErrApprovalStale) {
				t.Fatalf("unapproved canonical source published: %v", err)
			}
			assertUnstartedControl(t, s, "late-redirect", err)
			if action == "create" {
				list, _ := s.List()
				if len(list) != 0 {
					t.Fatal("unexpected library published")
				}
			} else {
				after, _ := os.ReadFile(configPath)
				if string(after) != string(before) {
					t.Fatal("changed source persisted after scope mismatch")
				}
			}
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(source+"-old", source); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ExecuteControl(action, args, "late-redirect", func(string) bool { return true }); err != nil {
				t.Fatalf("known rejection blocked a newly approved retry: %v", err)
			}
		})
	}
}

func assertUnstartedControl(t *testing.T, s *Service, key string, err error) {
	t.Helper()
	var failure *contracts.MutationError
	if errors.As(err, &failure) && failure.State != "not_started" {
		t.Fatalf("known rejection reported as %s: %v", failure.State, err)
	}
	path, pathErr := s.receiptPath(key)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("known rejection retained its receipt: %v", statErr)
	}
}

func TestLibraryControlDeletesDiagnosticsAndOrphansWithRevision(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		s := controlLibraryService(t)
		d, err := s.Create(Input{Name: "Library", SourcePath: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if orphan {
			err = os.RemoveAll(filepath.Join(s.root, d.ID))
		} else {
			err = os.WriteFile(filepath.Join(s.root, d.ID, "library.yml"), []byte("unknown: true\n"), 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
		diagnostic, err := s.Get(d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if diagnostic.BaseRevision == "" {
			t.Fatal("diagnostic has no deletion baseline")
		}
		args := map[string]any{"libraryId": d.ID, "baseRevision": diagnostic.BaseRevision}
		if _, err := s.ExecuteControl("delete", args, "cleanup", func(string) bool { return true }); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(d.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("diagnostic deletion failed: %v", err)
		}
	}
}
