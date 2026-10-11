package session

import (
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/runtimeskills"
)

func installRuntimeSkillFixture(t *testing.T, runtimeDir, id string) {
	t.Helper()
	source := filepath.Join(runtimeDir, "skills", filepath.FromSlash(id))
	digest, err := runtimeskills.Digest(source)
	if err != nil {
		t.Fatal(err)
	}
	root := runtimeskills.Root(filepath.Dir(filepath.Dir(runtimeDir)))
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	dir, _ := runtimeskills.Path(root, digest)
	if err := os.Rename(source, dir); err != nil {
		t.Fatal(err)
	}
	if err := runtimeskills.WriteReferences(runtimeDir, []runtimeskills.Reference{{ID: id, Digest: digest}}); err != nil {
		t.Fatal(err)
	}
}
