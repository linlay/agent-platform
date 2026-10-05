package kbx

import (
	"agent-platform/internal/builtins"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCenterRealCLI(t *testing.T) {
	bin := os.Getenv("KBX_CENTER_TEST_BIN")
	if bin == "" {
		t.Skip("set KBX_CENTER_TEST_BIN to managed bin directory")
	}
	t.Setenv("AP_BUILTINS_BIN", bin)
	if _, err := builtins.ConfigureProcessPath(); err != nil {
		t.Fatal(err)
	}
	e := NewCenterEngine()
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	source, _ = filepath.EvalSymlinks(source)
	doc := filepath.Join(source, "note.txt")
	os.WriteFile(doc, []byte("The zebra retrieval fixture."), 0600)
	db := filepath.Join(root, "library", "index.sqlite")
	os.Mkdir(filepath.Dir(db), 0700)
	for i := 0; i < 2; i++ {
		if err := e.Update(ctx, db, source); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}
	raw, err := e.Read(ctx, db, "search", "zebra", 5)
	if err != nil {
		t.Fatal(err)
	}
	var found searchResponse
	if err = json.Unmarshal(raw, &found); err != nil || len(found.Results) != 1 {
		t.Fatalf("search %s %v", raw, err)
	}
	raw, err = e.Read(ctx, db, "read", found.Results[0].File, 0)
	if err != nil {
		t.Fatal(err)
	}
	var read struct{ Body string }
	json.Unmarshal(raw, &read)
	if read.Body != "The zebra retrieval fixture." {
		t.Fatalf("read %s", raw)
	}
	os.WriteFile(doc, []byte("An elephant replaces the earlier document."), 0600)
	if err = e.Update(ctx, db, source); err != nil {
		t.Fatal(err)
	}
	raw, err = e.Read(ctx, db, "search", "elephant", 5)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &found)
	if len(found.Results) != 1 {
		t.Fatalf("updated document absent: %s", raw)
	}
	os.Remove(doc)
	if err = e.Update(ctx, db, source); err != nil {
		t.Fatal(err)
	}
	raw, err = e.Read(ctx, db, "files", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct{ Documents []any }
	json.Unmarshal(raw, &inventory)
	if len(inventory.Documents) != 0 {
		t.Fatalf("deleted source still indexed: %s", raw)
	}
}
