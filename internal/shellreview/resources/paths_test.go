package resources

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A file matched by one wildcard (such as .DS_Store) cannot be descended by
// the next segment; bash skips it, so expansion must not fail on it.
func TestExpandOperandPathsSkipsFilesBetweenWildcards(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "repo", "app", "node_modules")
	if err := os.MkdirAll(want, 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{filepath.Join(root, ".DS_Store"), filepath.Join(root, "repo", "notes.txt")} {
		if err := os.WriteFile(file, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The second pattern puts a literal segment after the file, so opening
	// .DS_Store/app fails with ENOTDIR instead of reading a file.
	for _, pattern := range []string{"*/*/node_modules", "*/app/*"} {
		got, err := ExpandOperandPaths(filepath.Join(root, pattern))
		if err != nil {
			t.Fatalf("%s: %v", pattern, err)
		}
		if !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("%s: got %v, want %v", pattern, got, []string{want})
		}
	}
}
