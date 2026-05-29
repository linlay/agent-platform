package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func TestMaybeSpillToolResultLeavesSmallResultInline(t *testing.T) {
	chatDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID: "chat-small",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatAttachmentsDir: chatDir},
			},
		},
	}
	result := contracts.ToolExecutionResult{
		Output:     `{"kind":"text","content":"hello"}`,
		Structured: map[string]any{"kind": "text", "content": "hello"},
	}

	got := stream.maybeSpillToolResult(&preparedToolInvocation{toolID: "call_small", toolName: "file_read"}, result)

	if _, ok := got.Structured["resultRef"]; ok {
		t.Fatalf("did not expect resultRef for small result: %#v", got.Structured)
	}
	if _, err := os.Stat(filepath.Join(chatDir, chat.ToolRootDirName, chat.ToolResultsDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected no tool result dir, stat err=%v", err)
	}
}

func TestMaybeSpillToolResultWritesFullResultAndReturnsPreview(t *testing.T) {
	chatDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID: "chat-large",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatAttachmentsDir: chatDir},
			},
		},
	}
	content := strings.Repeat("x", toolResultSpillThresholdBytes+1024)
	full := map[string]any{
		"kind":     "text",
		"filePath": "/tmp/large.txt",
		"content":  content,
	}
	resultJSON, _ := json.Marshal(full)
	result := contracts.ToolExecutionResult{
		Output:     string(resultJSON),
		Structured: full,
	}

	got := stream.maybeSpillToolResult(&preparedToolInvocation{toolID: "call_large", toolName: "file_read"}, result)

	ref, ok := got.Structured["resultRef"].(map[string]any)
	if !ok {
		t.Fatalf("expected resultRef, got %#v", got.Structured)
	}
	if ref["path"] != ".tools/results/call_large.json" {
		t.Fatalf("unexpected ref path: %#v", ref)
	}
	sum := sha256.Sum256(resultJSON)
	if ref["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected sha: %#v", ref)
	}
	if ref["sizeBytes"] != len(resultJSON) {
		t.Fatalf("unexpected sizeBytes: %#v", ref)
	}
	if got.Structured["truncated"] != true {
		t.Fatalf("expected truncated marker, got %#v", got.Structured)
	}
	preview, _ := got.Structured["content"].(string)
	if len(preview) >= len(content) || !strings.Contains(preview, "see resultRef") {
		t.Fatalf("expected shortened preview, got len=%d", len(preview))
	}

	data, err := os.ReadFile(filepath.Join(chatDir, chat.ToolRootDirName, chat.ToolResultsDirName, "call_large.json"))
	if err != nil {
		t.Fatalf("read spilled result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode spilled result: %v", err)
	}
	if decoded["content"] != content {
		t.Fatalf("spilled content mismatch")
	}
}

func TestMaybeSpillToolResultConvertsLargePlainBashOutputToStructuredPreview(t *testing.T) {
	chatDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID: "chat-bash",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatAttachmentsDir: chatDir},
			},
		},
	}
	stdout := strings.Repeat("z", toolResultSpillThresholdBytes+1024)

	got := stream.maybeSpillToolResult(&preparedToolInvocation{toolID: "call/bash", toolName: "bash"}, contracts.ToolExecutionResult{
		Output:   stdout,
		ExitCode: 0,
	})

	ref, ok := got.Structured["resultRef"].(map[string]any)
	if !ok {
		t.Fatalf("expected structured preview with resultRef, got %#v", got)
	}
	if ref["path"] != ".tools/results/call_bash.json" {
		t.Fatalf("unexpected sanitized path: %#v", ref)
	}
	preview, _ := got.Structured["stdout"].(string)
	if len(preview) >= len(stdout) {
		t.Fatalf("expected stdout preview, got len=%d", len(preview))
	}
	data, err := os.ReadFile(filepath.Join(chatDir, chat.ToolRootDirName, chat.ToolResultsDirName, "call_bash.json"))
	if err != nil {
		t.Fatalf("read spilled bash result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode spilled bash result: %v", err)
	}
	if decoded["stdout"] != stdout {
		t.Fatalf("spilled stdout mismatch")
	}
}
