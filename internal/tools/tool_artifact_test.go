package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishArtifactsUsesSourceBasenameAndIgnoresName(t *testing.T) {
	workspace := t.TempDir()
	restoreCwd := chdirForArtifactTest(t, workspace)
	defer restoreCwd()
	workspace = mustGetwd(t)

	chatsRoot := filepath.Join(workspace, "chats")
	sourcePath := filepath.Join(workspace, "服务前端全部迁移为 Webview.md")
	if err := os.WriteFile(sourcePath, []byte("# report\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	published := publishArtifacts(chatsRoot, "chat-1", "run-1", []any{
		map[string]any{
			"path": sourcePath,
			"name": "服务前端全部迁移为 Webview",
		},
	})
	if len(published) != 1 {
		t.Fatalf("expected 1 published artifact, got %#v", published)
	}

	item := published[0]
	if item["name"] != "服务前端全部迁移为 Webview.md" {
		t.Fatalf("expected source basename with extension, got %#v", item["name"])
	}
	if item["mimeType"] != "text/markdown" {
		t.Fatalf("expected markdown MIME type, got %#v", item["mimeType"])
	}
	if !strings.Contains(item["url"].(string), "Webview.md") {
		t.Fatalf("expected URL to include .md filename, got %#v", item["url"])
	}
	if _, err := os.Stat(filepath.Join(chatsRoot, "chat-1", "artifacts", "run-1", "服务前端全部迁移为 Webview.md")); err != nil {
		t.Fatalf("expected copied artifact with source basename: %v", err)
	}
}

func TestPublishArtifactsKeepsExtensionlessSourceName(t *testing.T) {
	workspace := t.TempDir()
	restoreCwd := chdirForArtifactTest(t, workspace)
	defer restoreCwd()
	workspace = mustGetwd(t)

	chatsRoot := filepath.Join(workspace, "chats")
	sourcePath := filepath.Join(workspace, "README")
	if err := os.WriteFile(sourcePath, []byte("plain text\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	published := publishArtifacts(chatsRoot, "chat-1", "run-1", []any{
		map[string]any{"path": sourcePath},
	})
	if len(published) != 1 {
		t.Fatalf("expected 1 published artifact, got %#v", published)
	}

	item := published[0]
	if item["name"] != "README" {
		t.Fatalf("expected extensionless source basename, got %#v", item["name"])
	}
	if item["mimeType"] != "application/octet-stream" {
		t.Fatalf("expected octet-stream MIME type, got %#v", item["mimeType"])
	}
}

func TestArtifactPublishSchemaDoesNotExposeName(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "resources", "tools", "artifact_publish.yml"))
	if err != nil {
		t.Fatalf("read artifact schema: %v", err)
	}
	schema := string(data)
	if strings.Contains(schema, "          name:") {
		t.Fatalf("artifact_publish schema should not expose artifacts[].name")
	}
	if !strings.Contains(schema, "        additionalProperties: false") {
		t.Fatalf("artifact_publish item schema should keep additionalProperties=false")
	}
}

func chdirForArtifactTest(t *testing.T, dir string) func() {
	t.Helper()

	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	return dir
}
