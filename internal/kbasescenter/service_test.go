package kbasescenter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testEngine struct {
	release chan struct{}
	err     error
}

func (e testEngine) Update(ctx context.Context, db, source string) error {
	if e.release != nil {
		select {
		case <-e.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if e.err != nil {
		return e.err
	}
	return os.WriteFile(db, []byte("index"), 0600)
}
func (e testEngine) Read(context.Context, string, string, string, int) (json.RawMessage, error) {
	return json.RawMessage(`{"results":[]}`), nil
}
func waitState(t *testing.T, s *Service, id, want string) Definition {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if d.State == want {
			return d
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("state did not reach " + want)
	return Definition{}
}
func TestLibraryLifecycle(t *testing.T) {
	root := t.TempDir()
	source := t.TempDir()
	release := make(chan struct{})
	s, err := New(context.Background(), root, testEngine{release: release})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.Create(Input{Name: "Docs", SourcePath: source})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Read(context.Background(), d.ID, "search", "x", 5); err == nil {
		t.Fatal("unindexed search accepted")
	}
	if _, err = s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(d.ID); !errors.Is(err, ErrBusy) {
		t.Fatal("deleted indexing library", err)
	}
	if _, err = s.Refresh(d.ID); !errors.Is(err, ErrBusy) {
		t.Fatal("duplicate refresh accepted", err)
	}
	close(release)
	waitState(t, s, d.ID, "ready")
	if _, err = s.Edit(d.ID, Input{Name: "Renamed", Description: "Notes"}); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(context.Background(), root, testEngine{})
	if err != nil {
		t.Fatal(err)
	}
	items, err := reopened.List()
	if err != nil || len(items) != 1 || items[0].Name != "Renamed" {
		t.Fatalf("persistence: %+v %v", items, err)
	}
	if _, err = s.Read(context.Background(), d.ID, "search", "x", 51); err == nil {
		t.Fatal("invalid limit accepted")
	}
	if _, err = s.Read(context.Background(), d.ID, "read", "kbx://other/secret", 0); err == nil {
		t.Fatal("foreign collection accepted")
	}
	if err = s.Delete(d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(source); err != nil {
		t.Fatal("source removed")
	}
	if _, err = s.Get(d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestLibraryValidationAndRecovery(t *testing.T) {
	root := t.TempDir()
	s, _ := New(context.Background(), root, testEngine{err: errors.New("failed")})
	for _, source := range []string{"relative", root, filepath.Dir(root)} {
		if _, err := s.Create(Input{Name: "bad", SourcePath: source}); err == nil {
			t.Fatal("unsafe source accepted", source)
		}
	}
	if _, err := s.Get("../escape"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	d, err := s.Create(Input{Name: "Valid", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, d.ID, "error")
	d.State = "indexing"
	if err = s.save(d); err != nil {
		t.Fatal(err)
	}
	recovered, _ := s.Get(d.ID)
	if recovered.State != "error" {
		t.Fatal("interrupted build reported running")
	}
	if err = os.RemoveAll(filepath.Join(root, d.ID)); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(t.TempDir(), filepath.Join(root, d.ID)); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(d.ID); err == nil {
		t.Fatal("followed library symlink")
	}
}
