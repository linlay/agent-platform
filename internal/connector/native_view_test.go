package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeNativeViewPackage(t *testing.T, root, id string) {
	t.Helper()
	if err := WriteBuiltin(filepath.Join(root, id), strings.TrimPrefix(id, "builtin."), ""); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedNativeConnectorsDeclareViewQueriesTheyOwn(t *testing.T) {
	root := t.TempDir()
	for id, want := range map[string]string{TaskControlConnectorID: "automations", PlatformControlConnectorID: "catalog"} {
		writeNativeViewPackage(t, root, id)
		pkg, err := Load(root, id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		definition, ok := pkg.Views[want]
		if !ok || !definition.Supports("display") || definition.Queries["list"].Adapter != "native" {
			t.Fatalf("%s views: %#v", id, pkg.Views)
		}
		if owner, ok := NativeViewOperationOwner(definition.Queries["list"].Operation); !ok || owner != id {
			t.Fatalf("%s operation owner %q", id, owner)
		}
	}
}

func TestViewQueriesCannotBorrowAnotherConnectorsOperation(t *testing.T) {
	root := t.TempDir()
	writeNativeViewPackage(t, root, TaskControlConnectorID)
	path := filepath.Join(root, TaskControlConnectorID, "view.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "automation.list", "catalog.list", 1)), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, TaskControlConnectorID); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("borrowed operation accepted: %v", err)
	}
	// Refreshing the package replaces stale view files.
	writeNativeViewPackage(t, root, TaskControlConnectorID)
	if _, err := Load(root, TaskControlConnectorID); err != nil {
		t.Fatalf("refresh kept stale view.json: %v", err)
	}
}
