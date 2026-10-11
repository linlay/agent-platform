package view

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Run the same policy corpus through the browser parser and the Go parser.
// CI can supply WEBCLIENT_SOURCE; a checkout without Node/WebClient still runs
// the Go policy, protocol and immutable-snapshot tests.
func TestAskUserFormBrowser(t *testing.T) {
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
	page, err := BuiltinDocument("ask_user_form")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := os.ReadFile("../formhtml/testdata/forms.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"html": page.HTML, "cases": json.RawMessage(cases)})
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "page.json")
	if err := os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(node, "testdata/ask_user_form.cjs", jsdom, input).CombinedOutput()
	if err != nil {
		t.Fatalf("browser regression: %v\n%s", err, output)
	}
}
