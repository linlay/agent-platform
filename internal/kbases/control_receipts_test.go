package kbases

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLibraryControlReceiptRetentionPreservesUncertainResults(t *testing.T) {
	s := controlLibraryService(t)
	args := createArguments(t.TempDir())
	result, err := s.ExecuteControl("create", args, "completed", func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-controlReceiptRetention - time.Hour)
	completed, _ := s.receiptPath("completed")
	if err := os.Chtimes(completed, old, old); err != nil {
		t.Fatal(err)
	}
	seed := func(key, state string, age time.Time, valid bool) string {
		t.Helper()
		path, _ := s.receiptPath(key)
		b := []byte("invalid receipt")
		if valid {
			b, _ = json.Marshal(controlReceipt{Plan: ControlPlan{RequestDigest: "digest"}, State: state, Result: result})
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, age, age); err != nil {
			t.Fatal(err)
		}
		return path
	}
	retained := []string{seed("recent", "completed", now, true), seed("interrupted", "started", old, true), seed("corrupt", "completed", old, false)}
	unrelated := filepath.Join(filepath.Dir(completed), "notes.json")
	if err := os.WriteFile(unrelated, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	retained = append(retained, unrelated)
	link, _ := s.receiptPath("symlink")
	if err := os.Symlink(retained[0], link); err != nil {
		t.Fatal(err)
	}
	retained = append(retained, link)
	if err := s.cleanupControlReceipts(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(completed); !os.IsNotExist(err) {
		t.Fatalf("expired completed receipt retained: %v", err)
	}
	for _, path := range retained {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("cleanup removed unknown/recent/unrelated data %s: %v", path, err)
		}
	}
	if _, err := s.Get(result["libraryId"].(string)); err != nil {
		t.Fatalf("receipt cleanup damaged the library: %v", err)
	}
	throttled := seed("next-cleanup", "completed", old, true)
	if err := s.cleanupControlReceipts(now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(throttled); err != nil {
		t.Fatalf("cleanup was not throttled: %v", err)
	}
	if err := s.cleanupControlReceipts(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(throttled); !os.IsNotExist(err) {
		t.Fatalf("hourly cleanup did not resume: %v", err)
	}
}
