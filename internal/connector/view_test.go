package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestViewPackageValidationSummaryAndDefinitionSave(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "forms")
	if err := os.MkdirAll(filepath.Join(dir, "views"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"connector.json":   `{"id":"forms","name":"Forms","version":"1.0.0","type":"view","auth_mode":"none"}`,
		"view.json":        `{"views":{"edit":{"renderer":"html","entry":"views/index.html","usage":["form","display"]}}}`,
		"views/index.html": "<form>example</form>",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := Load(root, "forms")
	if err != nil || len(pkg.Views) != 1 || len(pkg.MCP) != 0 || pkg.CLI != nil || pkg.BinDir != "" {
		t.Fatalf("view package: %#v %v", pkg, err)
	}
	items, err := (Sources{ExternalRoot: root}).Summaries()
	if err != nil || !items[0].HasView || items[0].Views[0].Key != "edit" {
		t.Fatalf("summary: %#v %v", items, err)
	}
	file, err := ReadFile(root, "forms", "view.json")
	if err != nil {
		t.Fatal(err)
	}
	invalid := file
	invalid.Content = strings.Replace(file.Content, "views/index.html", "../secret", 1)
	if _, err := SaveDefinition(root, invalid, file.SHA256, nil, nil); err == nil {
		t.Fatal("invalid view definition published")
	}
	current, _ := ReadFile(root, "forms", "view.json")
	if current.SHA256 != file.SHA256 {
		t.Fatal("invalid save changed source")
	}
	valid := file
	valid.Content = strings.Replace(file.Content, `"form","display"`, `"display"`, 1)
	if _, err := SaveDefinition(root, valid, file.SHA256, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "view.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "forms"); err == nil {
		t.Fatal("pure view package without view.json accepted")
	}
}

func TestViewHeadersDoNotReadProcessEnvironment(t *testing.T) {
	t.Setenv("VIEW_PRIVATE_TOKEN", "process-secret")
	if _, err := ResolveViewHeaders(t.TempDir(), "remote", map[string]string{"Authorization": "Bearer ${VIEW_PRIVATE_TOKEN}"}); err == nil {
		t.Fatal("read process env")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "remote")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"VIEW_PRIVATE_TOKEN":"deployment-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	headers, err := ResolveViewHeaders(root, "remote", map[string]string{"Authorization": "Bearer ${VIEW_PRIVATE_TOKEN}"})
	if err != nil || headers["Authorization"] != "Bearer deployment-secret" {
		t.Fatalf("headers: %#v %v", headers, err)
	}
}
