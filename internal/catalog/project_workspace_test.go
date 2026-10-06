package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectWorkspaceRequiresSpecificExistingDirectory(t *testing.T) {
	workspace := t.TempDir()
	file := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(file, []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.VolumeName(workspace) + string(os.PathSeparator)
	for _, tc := range []struct {
		name, path string
		valid      bool
	}{
		{"empty", "", false},
		{"host root", "@root", false},
		{"chat alias", "@chat", false},
		{"filesystem root", root, false},
		{"relative directory", ".", false},
		{"missing directory", filepath.Join(workspace, "missing"), false},
		{"file", file, false},
		{"empty project directory", workspace, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := map[string]any{"runtimeConfig": map[string]any{"workspaceRoot": tc.path}}
			if err := ValidateProjectWorkspace(definition); (err == nil) != tc.valid {
				t.Fatalf("workspace %q: %v", tc.path, err)
			}
		})
	}
	if err := ValidateProjectWorkspace(nil); err == nil {
		t.Fatal("missing definition accepted")
	}

	link := filepath.Join(workspace, "root-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("cannot create directory symlink: %v", err)
	}
	if err := ValidateProjectWorkspace(map[string]any{"runtimeConfig": map[string]any{"workspaceRoot": link}}); err == nil {
		t.Fatal("symlink to filesystem root accepted as a project")
	}
}
