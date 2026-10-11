package kbx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/builtins"
	"agent-platform/internal/kbases"
)

func TestLibraryRealSplitStorageLifecycle(t *testing.T) {
	bin := os.Getenv("KBX_KBASES_TEST_BIN")
	if bin == "" {
		t.Skip("set KBX_KBASES_TEST_BIN to managed bin directory")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "documents")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "note.md"), []byte("# Fixture\nquartzorchid split storage verification.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeRoot := filepath.Join(root, "ru-kbases")
	service, err := kbases.New(ctx, filepath.Join(root, "kbases"), runtimeRoot, NewLibraryEngine())
	if err != nil {
		t.Fatal(err)
	}
	d, err := service.Create(kbases.Input{Name: "Fixture", Collections: []kbases.Collection{{Name: "docs", SourcePath: source}}})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "kbases", d.ID, "library.yml")
	initial, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		if _, err := service.Refresh(d.ID); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			current, err := service.Get(d.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.State == "error" {
				t.Fatal(current.Error)
			}
			if current.State == "ready" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("index deadline exceeded")
			}
			time.Sleep(20 * time.Millisecond)
		}
		result, err := service.Search(ctx, d.ID, kbases.SearchInput{Query: "quartzorchid", Method: "search"})
		if err != nil || !strings.Contains(string(result), "quartzorchid") {
			t.Fatalf("search: %s %v", result, err)
		}
		actual, err := os.ReadFile(configPath)
		if err != nil || string(actual) != string(initial) {
			t.Fatal("KBX changed desired configuration", err)
		}
		if _, err := os.Stat(filepath.Join(runtimeRoot, d.ID, "index.sqlite")); err != nil {
			t.Fatal(err)
		}
		if pass == 0 {
			if err := os.Rename(source, source+"-offline"); err != nil {
				t.Fatal(err)
			}
			offline, err := service.Get(d.ID)
			if err != nil || offline.State != "ready" || len(offline.SourceWarnings) == 0 {
				t.Fatalf("offline status: %+v %v", offline, err)
			}
			result, err := service.Search(ctx, d.ID, kbases.SearchInput{Query: "quartzorchid", Method: "search"})
			if err != nil || !strings.Contains(string(result), "quartzorchid") {
				t.Fatalf("offline search: %s %v", result, err)
			}
			result, err = service.Read(ctx, d.ID, "read", "kbx://docs/note.md", 0)
			if err != nil || !strings.Contains(string(result), "quartzorchid") {
				t.Fatalf("offline read: %s %v", result, err)
			}
			if _, err := service.Refresh(d.ID); err == nil {
				t.Fatal("refreshed offline source")
			}
			if err := os.Rename(source+"-offline", source); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(runtimeRoot); err != nil {
				t.Fatal(err)
			}
			current, err := service.Get(d.ID)
			if err != nil || current.State != "unindexed" {
				t.Fatalf("missing runtime: %+v %v", current, err)
			}
		}
	}
	if err := service.Delete(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "note.md")); err != nil {
		t.Fatal("source deleted", err)
	}
}
