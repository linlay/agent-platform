package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

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
	if len(preview) > toolResultPreviewBytes {
		t.Fatalf("expected preview to fit %d bytes, got %d", toolResultPreviewBytes, len(preview))
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

func TestMaybeSpillToolResultStaysInlineInReadOnlyMode(t *testing.T) {
	chatDir := t.TempDir()
	stream := &llmRunStream{
		execCtx: &contracts.ExecutionContext{ToolExecutionPolicy: contracts.ToolExecutionPolicyReadOnly},
		session: contracts.QuerySession{
			ChatID: "chat-btw-large",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatAttachmentsDir: chatDir},
			},
		},
	}
	content := strings.Repeat("x", toolResultSpillThresholdBytes+1024)
	result := contracts.ToolExecutionResult{Output: content, Structured: map[string]any{"content": content}}
	got := stream.maybeSpillToolResult(&preparedToolInvocation{toolID: "call_large", toolName: "file_read"}, result)
	if !reflect.DeepEqual(got.Structured, result.Structured) {
		t.Fatalf("expected full inline result, got %#v", got.Structured)
	}
	if _, err := os.Stat(filepath.Join(chatDir, chat.ToolRootDirName)); !os.IsNotExist(err) {
		t.Fatalf("read-only mode wrote tool result spill: %v", err)
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

func TestMaybeSpillToolResultPreviewPreservesUTF8(t *testing.T) {
	chatDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID: "chat-utf8",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatAttachmentsDir: chatDir},
			},
		},
	}
	content := strings.Repeat("你好", toolResultSpillThresholdBytes)
	full := map[string]any{"kind": "text", "content": content}
	resultJSON, _ := json.Marshal(full)

	got := stream.maybeSpillToolResult(&preparedToolInvocation{toolID: "call_utf8", toolName: "file_read"}, contracts.ToolExecutionResult{
		Output:     string(resultJSON),
		Structured: full,
	})

	preview, _ := got.Structured["content"].(string)
	if !utf8.ValidString(preview) {
		t.Fatalf("expected valid UTF-8 preview")
	}
	if len(preview) > toolResultPreviewBytes {
		t.Fatalf("expected preview to fit %d bytes, got %d", toolResultPreviewBytes, len(preview))
	}
	if got.Structured["truncated"] != true {
		t.Fatalf("expected truncated marker, got %#v", got.Structured)
	}
}

func TestMaybeSpillToolResultIncludesRegexAndVisionRecognize(t *testing.T) {
	for _, toolName := range []string{"regex", "vision_recognize"} {
		t.Run(toolName, func(t *testing.T) {
			chatDir := t.TempDir()
			stream := &llmRunStream{
				session: contracts.QuerySession{
					ChatID: "chat-" + toolName,
					RuntimeContext: contracts.RuntimeRequestContext{
						LocalPaths: contracts.LocalPaths{ChatAttachmentsDir: chatDir},
					},
				},
			}
			output := strings.Repeat("m", toolResultSpillThresholdBytes+1024)

			got := stream.maybeSpillToolResult(&preparedToolInvocation{toolID: "call_" + toolName, toolName: toolName}, contracts.ToolExecutionResult{
				Output: output,
			})

			if got.Structured["truncated"] != true {
				t.Fatalf("expected %s result to spill, got %#v", toolName, got.Structured)
			}
			if _, ok := got.Structured["resultRef"].(map[string]any); !ok {
				t.Fatalf("expected resultRef for %s, got %#v", toolName, got.Structured)
			}
			preview, _ := got.Structured["output"].(string)
			if len(preview) > toolResultPreviewBytes {
				t.Fatalf("expected %s preview to fit %d bytes, got %d", toolName, toolResultPreviewBytes, len(preview))
			}
		})
	}
}

func TestMaybeSpillToolResultSkipsNonEligibleTool(t *testing.T) {
	chatDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID: "chat-agent",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatAttachmentsDir: chatDir},
			},
		},
	}
	output := strings.Repeat("a", toolResultSpillThresholdBytes+1024)

	got := stream.maybeSpillToolResult(&preparedToolInvocation{toolID: "call_agent", toolName: "agent_invoke"}, contracts.ToolExecutionResult{
		Output: output,
	})

	if got.Output != output {
		t.Fatalf("expected non-eligible output to stay inline")
	}
	if len(got.Structured) != 0 {
		t.Fatalf("did not expect structured preview for non-eligible tool: %#v", got.Structured)
	}
	if _, err := os.Stat(filepath.Join(chatDir, chat.ToolRootDirName, chat.ToolResultsDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected no tool result dir for non-eligible tool, stat err=%v", err)
	}
}

func TestMaybeSpillToolResultSkipsRawParams(t *testing.T) {
	chatDir := t.TempDir()
	stream := &llmRunStream{
		session: contracts.QuerySession{
			ChatID: "chat-raw",
			RuntimeContext: contracts.RuntimeRequestContext{
				LocalPaths: contracts.LocalPaths{ChatAttachmentsDir: chatDir},
			},
		},
	}
	content := strings.Repeat("r", toolResultSpillThresholdBytes+1024)
	full := map[string]any{"kind": "text", "content": content}
	resultJSON, _ := json.Marshal(full)
	result := contracts.ToolExecutionResult{
		Output:     string(resultJSON),
		Structured: full,
		RawParams:  map[string]any{"content": content},
	}

	got := stream.maybeSpillToolResult(&preparedToolInvocation{toolID: "call_raw", toolName: "file_read"}, result)

	if _, ok := got.Structured["resultRef"]; ok {
		t.Fatalf("did not expect resultRef for RawParams result: %#v", got.Structured)
	}
	if got.Output != result.Output {
		t.Fatalf("expected RawParams output to stay inline")
	}
	if _, err := os.Stat(filepath.Join(chatDir, chat.ToolRootDirName, chat.ToolResultsDirName)); !os.IsNotExist(err) {
		t.Fatalf("expected no tool result dir for RawParams result, stat err=%v", err)
	}
}
