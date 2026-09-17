package catalog_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agent-platform/internal/catalog"
	runtimewatch "agent-platform/internal/watch"
)

func TestConnectorImportStagingDoesNotBlockPublication(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "created-after-watch"
		if existing {
			name = "present-before-watch"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			stage := filepath.Join(root, ".connector-import-123")
			candidate := filepath.Join(stage, "demo")
			create := func() {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(candidate, "skills"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(candidate, "connector.json"), []byte(`{}`), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if existing {
				create()
			}
			ctx, cancel := context.WithCancel(context.Background())
			events := make(chan runtimewatch.Event, 64)
			w, err := runtimewatch.Start(ctx, runtimewatch.Spec{
				Roots: []runtimewatch.Root{{Path: root, Recursive: true, ShouldTraverse: func(path string) bool {
					return catalog.ShouldWatchRuntimeDir(filepath.Base(path))
				}}},
				Ignore: catalog.ShouldIgnoreRuntimeWatchPath,
				OnEvent: func(event runtimewatch.Event) {
					select {
					case events <- event:
					default:
					}
				},
			})
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			defer func() { cancel(); <-w.Done() }()
			if !existing {
				create()
			}
			select {
			case event := <-events:
				t.Fatalf("unpublished package emitted event: %s", event.Path)
			case <-time.After(150 * time.Millisecond):
			}
			if count := w.Watched(); count != 1 {
				t.Fatalf("staging directories were watched: %d", count)
			}
			published := filepath.Join(root, "demo")
			if err := os.Rename(candidate, published); err != nil {
				t.Fatalf("publish connector: %v", err)
			}
			select {
			case event := <-events:
				if event.Path != published {
					t.Fatalf("unexpected publication event: %s", event.Path)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("published connector was not observed")
			}
			file := filepath.Join(published, "skills", "SKILL.md")
			if err := os.WriteFile(file, []byte("published skill"), 0644); err != nil {
				t.Fatal(err)
			}
			deadline := time.After(3 * time.Second)
			for {
				select {
				case event := <-events:
					if event.Path == file {
						return
					}
				case <-deadline:
					t.Fatal("published connector descendants are not watched")
				}
			}
		})
	}
}
