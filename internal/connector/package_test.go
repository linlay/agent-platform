package connector

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuiltinPackageIncludesBinAndSkills(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "builtin.dbx")
	if err := WriteBuiltin(dir, "dbx", "v1.2.3", "darwin"); err != nil {
		t.Fatal(err)
	}
	pkg, err := Load(root, "builtin.dbx")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Version != "1.2.3" || pkg.BinDir != filepath.Join(dir, "bin") || len(pkg.Skills) != 1 || pkg.Skills[0].Name != "builtin-dbx" {
		t.Fatalf("unexpected package: %#v", pkg)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "libs")); err != nil {
		t.Fatal(err)
	}
}

func TestPackageRejectsDuplicateJSONAndEscapes(t *testing.T) {
	for _, data := range []string{`{"id":"x","id":"y"}`, `{"mcpServers":{"main":{"type":"stdio","type":"streamableHttp"}}}`, `{} {}`, "{\"id\":\"\xff\"}"} {
		var value map[string]any
		if err := DecodeJSON([]byte(data), &value); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	root := t.TempDir()
	dir := filepath.Join(root, "builtin.dbx")
	if err := WriteBuiltin(dir, "dbx", "1.0.0", "darwin"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "bin", "outside")); err != nil {
		t.Skip(err)
	}
	if _, err := Load(root, "builtin.dbx"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escape accepted: %v", err)
	}
}

func TestInvalidDefinitionIsNeverPublished(t *testing.T) {
	root := t.TempDir()
	if err := WriteBuiltin(filepath.Join(root, "demo"), "dbx", "1.0.0", "darwin"); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "demo", "connector.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"id": "builtin.dbx"`, `"id": "demo"`, 1))
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	previous, err := ReadFile(root, "demo", "connector.json")
	if err != nil {
		t.Fatal(err)
	}
	input := previous
	input.Content = strings.Replace(input.Content, `"1.0.0"`, `"1.0.0-01"`, 1)
	reloaded := false
	if _, err := SaveDefinition(root, input, previous.SHA256, nil, func() error { reloaded = true; return nil }); err == nil {
		t.Fatal("accepted invalid candidate")
	}
	current, err := ReadFile(root, "demo", "connector.json")
	if err != nil || reloaded || current.SHA256 != previous.SHA256 {
		t.Fatalf("invalid candidate published: %v %v", reloaded, err)
	}
}

func TestAgentPathIsOrderedAndDoesNotChangeProcess(t *testing.T) {
	t.Setenv("PATH", "/system/bin")
	input := []string{"PATH=/system/bin", "HOME=/home/user"}
	got := WithPath(input, []string{"/connectors/a/bin", "/connectors/b/bin", "/connectors/a/bin"})
	want := []string{"HOME=/home/user", "PATH=/connectors/a/bin:/connectors/b/bin:/system/bin"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env=%v", got)
	}
	if os.Getenv("PATH") != "/system/bin" || input[0] != "PATH=/system/bin" {
		t.Fatal("modified process or caller environment")
	}
}
