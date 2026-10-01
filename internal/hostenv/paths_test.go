package hostenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendPathKeepsOrderAndSkipsDuplicatesOrMissing(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	env := []string{"PATH=" + a}
	got := Value(AppendPath(env, b, a, filepath.Join(b, "missing"), "relative"), "PATH")
	if got != a+string(os.PathListSeparator)+b {
		t.Fatalf("PATH=%s", got)
	}
	if Value(AppendPath([]string{}, a), "PATH") != a {
		t.Fatal("empty PATH")
	}
}
