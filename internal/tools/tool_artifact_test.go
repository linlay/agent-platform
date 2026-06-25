package tools

import (
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
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

	result := publishArtifacts(chatsRoot, "chat-1", "run-1", []any{
		map[string]any{
			"path": sourcePath,
			"name": "服务前端全部迁移为 Webview",
		},
	})
	if result.Status != "published" {
		t.Fatalf("expected published status, got %#v", result.Status)
	}
	published := result.PublishedArtifacts
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

	result := publishArtifacts(chatsRoot, "chat-1", "run-1", []any{
		map[string]any{"path": sourcePath},
	})
	published := result.PublishedArtifacts
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

func TestInvokeArtifactPublishReturnsErrorWhenNoArtifactsPublished(t *testing.T) {
	workspace := t.TempDir()
	restoreCwd := chdirForArtifactTest(t, workspace)
	defer restoreCwd()

	executor := &RuntimeToolExecutor{cfg: config.Config{}}
	executor.cfg.Paths.ChatsDir = filepath.Join(workspace, "chats")
	result, err := executor.invokeArtifactPublish(map[string]any{
		"artifacts": []any{
			map[string]any{"path": "/Users/example/Downloads/report.pptx"},
		},
	}, &ExecutionContext{Session: QuerySession{ChatID: "chat-1", RunID: "run-1"}})
	if err != nil {
		t.Fatalf("invoke artifact publish: %v", err)
	}
	if result.ExitCode == 0 || result.Error != "artifact_publish_failed" {
		t.Fatalf("expected failed tool result, got exit=%d error=%q", result.ExitCode, result.Error)
	}
	if result.Structured["status"] != "error" {
		t.Fatalf("expected error status, got %#v", result.Structured["status"])
	}
	assertCount(t, result.Structured["publishedCount"], 0)
	assertCount(t, result.Structured["failedCount"], 1)
	failures := result.Structured["failedArtifacts"].([]map[string]any)
	if failures[0]["code"] != "path_not_allowed" {
		t.Fatalf("expected path_not_allowed failure, got %#v", failures[0])
	}
}

func TestPublishArtifactsReportsMissingFile(t *testing.T) {
	workspace := t.TempDir()
	restoreCwd := chdirForArtifactTest(t, workspace)
	defer restoreCwd()

	chatsRoot := filepath.Join(workspace, "chats")
	result := publishArtifacts(chatsRoot, "chat-1", "run-1", []any{
		map[string]any{"path": "missing.txt"},
	})
	if result.Status != "error" {
		t.Fatalf("expected error status, got %#v", result.Status)
	}
	if len(result.PublishedArtifacts) != 0 || len(result.FailedArtifacts) != 1 {
		t.Fatalf("unexpected publish result: %#v", result)
	}
	if result.FailedArtifacts[0]["code"] != "file_not_found" {
		t.Fatalf("expected file_not_found failure, got %#v", result.FailedArtifacts[0])
	}
}

func TestPublishArtifactsReportsBatchFailureWhenAnyItemFails(t *testing.T) {
	workspace := t.TempDir()
	restoreCwd := chdirForArtifactTest(t, workspace)
	defer restoreCwd()
	workspace = mustGetwd(t)

	chatsRoot := filepath.Join(workspace, "chats")
	sourcePath := filepath.Join(workspace, "report.md")
	if err := os.WriteFile(sourcePath, []byte("# report\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	result := publishArtifacts(chatsRoot, "chat-1", "run-1", []any{
		map[string]any{"path": sourcePath},
		map[string]any{"path": "/Users/example/Downloads/report.pptx"},
	})
	if result.Status != "error" {
		t.Fatalf("expected error status, got %#v", result.Status)
	}
	if len(result.PublishedArtifacts) != 1 || len(result.FailedArtifacts) != 1 {
		t.Fatalf("unexpected failed batch result: %#v", result)
	}
	if result.FailedArtifacts[0]["code"] != "path_not_allowed" {
		t.Fatalf("expected path_not_allowed failure, got %#v", result.FailedArtifacts[0])
	}
}

func TestInvokeArtifactPublishHidesPublishedArtifactsWhenBatchFails(t *testing.T) {
	workspace := t.TempDir()
	restoreCwd := chdirForArtifactTest(t, workspace)
	defer restoreCwd()
	workspace = mustGetwd(t)

	sourcePath := filepath.Join(workspace, "report.md")
	if err := os.WriteFile(sourcePath, []byte("# report\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	executor := &RuntimeToolExecutor{cfg: config.Config{}}
	executor.cfg.Paths.ChatsDir = filepath.Join(workspace, "chats")
	result, err := executor.invokeArtifactPublish(map[string]any{
		"artifacts": []any{
			map[string]any{"path": sourcePath},
			map[string]any{"path": "/Users/example/Downloads/report.pptx"},
		},
	}, &ExecutionContext{Session: QuerySession{ChatID: "chat-1", RunID: "run-1"}})
	if err != nil {
		t.Fatalf("invoke artifact publish: %v", err)
	}
	if result.ExitCode == 0 || result.Error != "artifact_publish_failed" {
		t.Fatalf("expected failed tool result, got exit=%d error=%q", result.ExitCode, result.Error)
	}
	if result.Structured["status"] != "error" {
		t.Fatalf("expected error status, got %#v", result.Structured["status"])
	}
	assertCount(t, result.Structured["publishedCount"], 0)
	published := result.Structured["publishedArtifacts"].([]map[string]any)
	if len(published) != 0 {
		t.Fatalf("failed batch must not expose published artifacts, got %#v", published)
	}
}

func TestPublishArtifactsOverwritesSameRunArtifactFilename(t *testing.T) {
	workspace := t.TempDir()
	restoreCwd := chdirForArtifactTest(t, workspace)
	defer restoreCwd()
	workspace = mustGetwd(t)

	chatsRoot := filepath.Join(workspace, "chats")
	firstSource := filepath.Join(workspace, "first", "report.md")
	secondSource := filepath.Join(workspace, "second", "report.md")
	if err := os.MkdirAll(filepath.Dir(firstSource), 0o755); err != nil {
		t.Fatalf("mkdir first source: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(secondSource), 0o755); err != nil {
		t.Fatalf("mkdir second source: %v", err)
	}
	if err := os.WriteFile(firstSource, []byte("first\n"), 0o644); err != nil {
		t.Fatalf("write first source: %v", err)
	}
	if err := os.WriteFile(secondSource, []byte("second\n"), 0o644); err != nil {
		t.Fatalf("write second source: %v", err)
	}

	first := publishArtifacts(chatsRoot, "chat-1", "run-1", []any{
		map[string]any{"path": firstSource},
	})
	second := publishArtifacts(chatsRoot, "chat-1", "run-1", []any{
		map[string]any{"path": secondSource},
	})
	if first.Status != "published" || second.Status != "published" {
		t.Fatalf("expected both publishes to succeed, first=%#v second=%#v", first, second)
	}
	if first.PublishedArtifacts[0]["name"] != "report.md" || second.PublishedArtifacts[0]["name"] != "report.md" {
		t.Fatalf("expected overwrite to keep original filename, first=%#v second=%#v", first.PublishedArtifacts, second.PublishedArtifacts)
	}
	targetPath := filepath.Join(chatsRoot, "chat-1", "artifacts", "run-1", "report.md")
	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read overwritten artifact: %v", err)
	}
	if string(data) != "second\n" {
		t.Fatalf("expected second publish to overwrite target, got %q", string(data))
	}
	if _, err := os.Stat(filepath.Join(chatsRoot, "chat-1", "artifacts", "run-1", "report-1.md")); !os.IsNotExist(err) {
		t.Fatalf("did not expect deduplicated artifact suffix, stat err=%v", err)
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
	if !strings.Contains(schema, "publishedArtifacts") {
		t.Fatalf("artifact_publish schema should remind agents to trust publishedArtifacts")
	}
	if !strings.Contains(schema, "/Users/.../Downloads") {
		t.Fatalf("artifact_publish schema should warn against arbitrary local Downloads paths")
	}
}

func assertCount(t *testing.T, got any, want int) {
	t.Helper()
	switch typed := got.(type) {
	case int:
		if typed != want {
			t.Fatalf("expected count %d, got %#v", want, got)
		}
	case float64:
		if int(typed) != want {
			t.Fatalf("expected count %d, got %#v", want, got)
		}
	default:
		t.Fatalf("expected numeric count %d, got %#v", want, got)
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
