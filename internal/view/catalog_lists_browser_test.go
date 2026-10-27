package view

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIndependentCatalogListsBrowser(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	root := os.Getenv("WEBCLIENT_SOURCE")
	if root == "" {
		root = "../../../agent-webclient"
	}
	jsdom, err := filepath.Abs(filepath.Join(root, "node_modules/jsdom"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jsdom); err != nil {
		t.Skip("WebClient jsdom unavailable")
	}
	output, err := exec.Command(node, "testdata/catalog_lists.cjs", jsdom, "../resources/connectors/builtin.platform-control").CombinedOutput()
	if err != nil {
		t.Fatalf("catalog lists browser: %v\n%s", err, output)
	}
}
