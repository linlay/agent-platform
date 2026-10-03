package memory

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	return NewStore(filepath.Join(root, "memory"), filepath.Join(root, "owner"), time.UTC)
}

func TestFilesAreTheOnlySourceAndCASPreservesConcurrentChanges(t *testing.T) {
	s := testStore(t)
	for _, kind := range []string{"owner", "memory", "daily"} {
		date := ""
		if kind == "daily" {
			date = "2026-10-03"
		}
		d, err := s.Read(kind, date)
		if err != nil || d.Exists || d.Revision != "missing" {
			t.Fatalf("missing: %+v %v", d, err)
		}
		d, err = s.Save(kind, date, "中文記憶\n", d.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Save(kind, date, "stale", "missing"); !errors.Is(err, ErrConflict) {
			t.Fatalf("wanted conflict: %v", err)
		}
		dir, name, _ := s.target(kind, date)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("external edit"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Delete(kind, date, d.Revision); !errors.Is(err, ErrConflict) {
			t.Fatalf("delete lost edit: %v", err)
		}
		d, err = s.Read(kind, date)
		if err != nil || d.Content != "external edit" {
			t.Fatalf("read external: %+v %v", d, err)
		}
		if _, err = s.Delete(kind, date, d.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.MemoryDir, "memory.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected database")
	}
}

func TestConcurrentInstancesOnlyOneRevisionWins(t *testing.T) {
	s := testStore(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			other := NewStore(s.MemoryDir, s.OwnerDir, time.UTC)
			_, err := other.Save("memory", "", fmt.Sprint(i), "missing")
			if err == nil {
				wins.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Errorf("save: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("winners=%d", wins.Load())
	}
}

func TestConcurrentProcessesOnlyOneRevisionWins(t *testing.T) {
	if root := os.Getenv("AP_MEMORY_TEST_ROOT"); root != "" {
		s := NewStore(filepath.Join(root, "memory"), filepath.Join(root, "owner"), time.UTC)
		_, err := s.Save("memory", "", "verified fact", "missing")
		if err == nil {
			fmt.Println("MEMORY_WRITE_WON")
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(executable, "-test.run=^TestConcurrentProcessesOnlyOneRevisionWins$")
			cmd.Env = append(os.Environ(), "AP_MEMORY_TEST_ROOT="+root)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("child: %v %s", err, output)
			}
			if strings.Contains(string(output), "MEMORY_WRITE_WON") {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("process winners=%d", wins.Load())
	}
}

func TestRejectEscapesSymlinksAndOversizedFiles(t *testing.T) {
	s := testStore(t)
	for _, date := range []string{"../OWNER", "2026-02-30", "2026-1-01", "2026-10-03/../x"} {
		if _, err := s.Save("daily", date, "x", "missing"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %q: %v", date, err)
		}
	}
	if _, err := s.Save("memory", "", strings.Repeat("x", MaxFileBytes+1), "missing"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(external, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(s.MemoryDir, "memory.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := s.Read("memory", ""); err == nil {
		t.Fatal("read followed symlink")
	}
	if _, err := s.Save("memory", "", "overwritten", "missing"); err == nil {
		t.Fatal("write followed symlink")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(s.MemoryDir, "daily")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("daily", "2026-10-03"); err == nil {
		t.Fatal("followed daily symlink")
	}
	b, _ := os.ReadFile(external)
	if string(b) != "secret" {
		t.Fatal("external file changed")
	}
}

func TestDailyAppendSearchAndPagination(t *testing.T) {
	s := testStore(t)
	d, err := s.Append("2026-10-03", "- Verified outcome", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Append(d.Date, "- Follow up", d.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save("daily", "2026-10-02", "older verified outcome", "missing"); err != nil {
		t.Fatal(err)
	}
	dates, err := s.Dates("2026-10-03", 1)
	if err != nil || len(dates) != 1 || dates[0] != "2026-10-02" {
		t.Fatalf("dates=%v %v", dates, err)
	}
	matches, err := s.Search("VERIFIED", "")
	if err != nil || len(matches) != 2 {
		t.Fatalf("matches=%v %v", matches, err)
	}
	if _, err = s.Save("memory", "", strings.Repeat("中", 500), "missing"); err != nil {
		t.Fatal(err)
	}
	ctx, err := s.Context(256)
	if err != nil || !strings.Contains(ctx, "truncated") || strings.Contains(ctx, "Verified outcome") {
		t.Fatalf("context budget or logs: %v", err)
	}
}

func TestSearchPagesDoNotSkipDailyFilesAtMatchLimit(t *testing.T) {
	s := testStore(t)
	for i := 0; i < 75; i++ {
		date := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -i).Format(time.DateOnly)
		if _, err := s.Save("daily", date, strings.Repeat("match\n", 50), "missing"); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	before := ""
	for {
		page, err := s.SearchPage("match", before)
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range page.Matches {
			if seen[hit.Date] {
				t.Fatalf("duplicate date: %s", hit.Date)
			}
			seen[hit.Date] = true
		}
		if page.NextBefore == "" {
			break
		}
		if before == page.NextBefore {
			t.Fatal("cursor stuck")
		}
		before = page.NextBefore
	}
	if len(seen) != 75 {
		t.Fatalf("search skipped dates: %d", len(seen))
	}
}
