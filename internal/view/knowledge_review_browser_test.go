package view

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestKnowledgeLibraryReviewShowsGrantsAndKeepsApprovalData(t *testing.T) {
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
	if _, err = os.Stat(jsdom); err != nil {
		t.Skip("WebClient jsdom unavailable")
	}
	pages := map[string]string{}
	for _, key := range []string{"platform_control_review", "resource_delete_review"} {
		page, err := BuiltinDocument(key)
		if err != nil {
			t.Fatal(err)
		}
		pages[key] = page.HTML
	}
	raw, err := json.Marshal(pages)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "reviews.json")
	if err := os.WriteFile(input, raw, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(node, "testdata/knowledge_review.cjs", jsdom, input).CombinedOutput()
	if err != nil {
		t.Fatalf("knowledge review browser regression: %v\n%s", err, output)
	}
}
