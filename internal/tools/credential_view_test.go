package tools

import (
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/credentialview"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialFileReadViews(t *testing.T) {
	root := t.TempDir()
	executor := fileToolExecutor(root, true)
	executor.cfg.Providers.ExternalDir = filepath.Join(root, "registries", "providers")
	executor.cfg.IdentityFile = filepath.Join(root, "custom-identity")
	provider := filepath.Join(executor.cfg.Providers.ExternalDir, "demo.yml")
	if err := os.MkdirAll(filepath.Dir(provider), 0700); err != nil {
		t.Fatal(err)
	}
	raw := "key: demo\napiKey: private-api-key\nbaseUrl: https://example.test\n"
	if err := os.WriteFile(provider, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executor.cfg.IdentityFile, []byte("opaque-identity-token"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{provider, executor.cfg.IdentityFile} {
		result, err := executor.invokeRead(map[string]any{"file_path": file, "add_line_numbers": false}, fileToolExecutionContext(root))
		if err != nil || result.Error != "" {
			t.Fatal(err, result)
		}
		if strings.Contains(result.Output, "private-api-key") || strings.Contains(result.Output, "opaque-identity-token") {
			t.Fatal("credential leaked", result.Output)
		}
		if !strings.Contains(result.Output, credentialview.Hidden) {
			t.Fatal(result.Output)
		}
		if file == provider && !strings.Contains(result.Output, "https://example.test") {
			t.Fatal("public fields missing", result.Output)
		}
	}
	result, err := executor.invokeRead(map[string]any{"file_path": provider, "offset": 2, "limit": 1, "add_line_numbers": false}, fileToolExecutionContext(root))
	if err != nil || strings.Contains(result.Output, "private-api-key") {
		t.Fatal(err, result.Output)
	}
	saved, _ := os.ReadFile(provider)
	if string(saved) != raw {
		t.Fatal("execution source was modified")
	}
	alias := filepath.Join(root, "public-alias.yml")
	if err := os.Symlink(provider, alias); err == nil {
		result, err := executor.invokeRead(map[string]any{"file_path": alias}, fileToolExecutionContext(root))
		if err != nil || strings.Contains(result.Output, "private-api-key") {
			t.Fatal(err, result.Output)
		}
	}
}
func TestCredentialFileHistoryDoesNotCopySecrets(t *testing.T) {
	root := t.TempDir()
	store, err := chat.NewFileStoreAtStartup(filepath.Join(root, "chats"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	executor := fileToolExecutor(root, false)
	executor.chats = store
	executor.cfg.Providers.ExternalDir = filepath.Join(root, "providers")
	file := filepath.Join(executor.cfg.Providers.ExternalDir, "demo.yml")
	execCtx := &contracts.ExecutionContext{Session: contracts.QuerySession{ChatID: "chat-credentials", RunID: "run-credentials", WorkspaceRoot: root}}
	if err := executor.recordFileHistory(execCtx, file, []byte("key: demo\napiKey: old-secret\n"), true, []byte("key: demo\napiKey: new-secret\n"), true); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"original", "current"} {
		text, err := executor.ReadFileHistory("chat-credentials", "run-credentials", file, version)
		if err != nil || strings.Contains(text, "old-secret") || strings.Contains(text, "new-secret") || !strings.Contains(text, "key: demo") {
			t.Fatal(err, text)
		}
	}
}
