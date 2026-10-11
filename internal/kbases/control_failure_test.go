package kbases

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLibraryControlBusyRejectionCanRetry(t *testing.T) {
	for _, action := range []string{"update", "delete"} {
		for _, timing := range []string{"prepare", "before-approval", "after-approval"} {
			t.Run(action+"/"+timing, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				engine := &schedulerEngine{entered: entered, release: release}
				s, err := New(context.Background(), t.TempDir(), t.TempDir(), engine, Options{StateDir: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := s.Close(ctx); err != nil {
						t.Error(err)
					}
				})
				d, err := s.Create(Input{Name: "Original", SourcePath: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				args := map[string]any{"libraryId": d.ID, "baseRevision": d.BaseRevision}
				if action == "update" {
					args["patch"] = map[string]any{"description": "Updated"}
				}
				startIndex := func() {
					if _, err := s.Refresh(d.ID); err != nil {
						t.Fatal(err)
					}
					select {
					case <-entered:
					case <-time.After(5 * time.Second):
						t.Fatal("indexer did not start")
					}
				}
				if timing != "prepare" {
					if _, err := s.PrepareControl(action, args, "retry"); err != nil {
						t.Fatal(err)
					}
				}
				if timing != "after-approval" {
					startIndex()
				}
				approvals := 0
				if timing == "prepare" {
					_, err = s.PrepareControl(action, args, "retry")
				} else {
					_, err = s.ExecuteControl(action, args, "retry", func(string) bool {
						approvals++
						startIndex()
						return true
					})
				}
				if !errors.Is(err, ErrBusy) {
					t.Fatalf("busy library lost its real error: %v", err)
				}
				assertUnstartedControl(t, s, "retry", err)
				if timing != "after-approval" && approvals != 0 {
					t.Fatal("busy rejection consumed approval")
				}
				close(release)
				waitState(t, s, d.ID, "ready")
				result, err := s.ExecuteControl(action, args, "retry", func(string) bool { return true })
				if err != nil || result["libraryId"] != d.ID {
					t.Fatalf("same request could not retry after indexing: %+v %v", result, err)
				}
			})
		}
	}
}

func TestLibraryControlRevisionRejectionClearsReceipt(t *testing.T) {
	for _, action := range []string{"update", "delete"} {
		t.Run(action, func(t *testing.T) {
			s := controlLibraryService(t)
			d, err := s.Create(Input{Name: "Original", SourcePath: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"libraryId": d.ID, "baseRevision": d.BaseRevision}
			if action == "update" {
				args["patch"] = map[string]any{"name": "Updated"}
			}
			path := filepath.Join(s.root, d.ID, "library.yml")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := append(before, []byte("\n# concurrent external edit\n")...)
			_, err = s.ExecuteControl(action, args, "retry-revision", func(string) bool {
				if err := os.WriteFile(path, changed, 0600); err != nil {
					t.Fatal(err)
				}
				return true
			})
			if !errors.Is(err, ErrRevisionConflict) {
				t.Fatalf("lost revision conflict: %v", err)
			}
			assertUnstartedControl(t, s, "retry-revision", err)
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(changed) {
				t.Fatalf("rejected mutation changed concurrent edit: %v", err)
			}
			current, err := s.Get(d.ID)
			if err != nil {
				t.Fatal(err)
			}
			args["baseRevision"] = current.BaseRevision
			if _, err := s.ExecuteControl(action, args, "retry-revision", func(string) bool { return true }); err != nil {
				t.Fatalf("same identity could not use a freshly approved revision: %v", err)
			}
		})
	}
}

func TestLibraryControlReferenceRejectionClearsReceipt(t *testing.T) {
	s := controlLibraryService(t)
	d, err := s.Create(Input{Name: "Referenced", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	s.options.References = func(string) []string { return refs }
	args := map[string]any{"libraryId": d.ID, "baseRevision": d.BaseRevision}
	_, err = s.ExecuteControl("delete", args, "references", func(string) bool {
		refs = []string{"another-agent"}
		return true
	})
	var referenced *ReferencedError
	if !errors.As(err, &referenced) {
		t.Fatalf("lost reference rejection: %v", err)
	}
	assertUnstartedControl(t, s, "references", err)
	refs = nil
	if _, err := s.ExecuteControl("delete", args, "references", func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
}
