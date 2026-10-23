package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
)

func deepSeekFetchOutputFixture(t *testing.T, markdown, title string) *RuntimeToolExecutor {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertDeepSeekRequest(t, r, "/v1/tools/fetch")
		_ = json.NewEncoder(w).Encode(map[string]any{"title": title, "content": markdown})
	}))
	t.Cleanup(server.Close)
	return deepSeekWebTestExecutor(t, server)
}

func deepSeekFetchOutputContext(chatDir string) *contracts.ExecutionContext {
	return &contracts.ExecutionContext{Session: contracts.QuerySession{
		ToolNames: []string{"web_fetch", "file_read"}, ToolSetFrozen: true,
		RuntimeContext: contracts.RuntimeRequestContext{
			LocalPaths: contracts.LocalPaths{ChatDir: chatDir},
		},
	}}
}

func deepSeekFetchOutputInvoke(t *testing.T, executor *RuntimeToolExecutor, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, map[string]any) {
	t.Helper()
	result, err := executor.Invoke(context.Background(), "web_fetch", args, execCtx)
	if err != nil || result.Error != "" {
		t.Fatalf("fetch failed: err=%v result=%#v", err, result)
	}
	if len(result.Output) > contracts.ToolModelOutputMaxBytes || !utf8.ValidString(result.Output) || strings.ContainsRune(result.Output, utf8.RuneError) {
		t.Fatalf("invalid or oversized model preview: %d bytes", len(result.Output))
	}
	var preview map[string]any
	if err := json.Unmarshal([]byte(result.Output), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	return result, preview
}

func TestDeepSeekWebFetchLargeOutputSavesCompleteMarkdownForReadback(t *testing.T) {
	const tail = "完整原文的最后一行"
	markdown := strings.Repeat("中文资料 <tag> & 引用\n", 10000) + tail + "\n"
	executor := deepSeekFetchOutputFixture(t, markdown, "长文档")
	chatDir := t.TempDir()
	execCtx := deepSeekFetchOutputContext(chatDir)
	result, preview := deepSeekFetchOutputInvoke(t, executor, map[string]any{"url": "https://example.com/document"}, execCtx)
	if result.Structured["result"] != markdown || preview["contentTruncatedForLLM"] != true || preview["readbackAvailable"] != true {
		t.Fatalf("full evidence and recoverable model preview required: preview=%#v", preview)
	}
	if strings.Contains(contracts.AnyStringNode(preview["result"]), tail) {
		t.Fatal("expected a preview, not the complete long document")
	}
	if result.Structured["contentTruncated"] == true {
		t.Fatal("the complete Structured.result must not claim its content was discarded")
	}
	ref, ok := preview["resultRef"].(map[string]any)
	if !ok {
		t.Fatal("missing resultRef")
	}
	path := contracts.AnyStringNode(ref["path"])
	if !strings.HasPrefix(path, ".tools/web-fetch/") || !strings.HasSuffix(path, ".md") || filepath.IsAbs(path) {
		t.Fatalf("expected a Chat-relative Markdown path, got %q", path)
	}
	stored, err := os.ReadFile(filepath.Join(chatDir, filepath.FromSlash(path)))
	if err != nil || string(stored) != markdown {
		t.Fatalf("saved Markdown lost original text or tail: %v", err)
	}
	sum := sha256.Sum256(stored)
	if ref["sha256"] != hex.EncodeToString(sum[:]) || ref["sizeBytes"] != float64(len(stored)) || ref["contentType"] != "text/markdown" {
		t.Fatalf("incorrect resultRef metadata: %#v", ref)
	}
	// Prove the recovery advice works with the real file tool and line ranges.
	read, err := executor.Invoke(context.Background(), "file_read", map[string]any{
		"filePath": "@chat/" + path, "offset": 10001, "limit": 1,
	}, execCtx)
	if err != nil || read.Error != "" || !strings.Contains(read.Output, tail) {
		t.Fatalf("could not read the omitted tail: err=%v result=%#v", err, read)
	}
}

func TestDeepSeekWebFetchPreviewHonorsSmallProfileLimit(t *testing.T) {
	markdown := "# 标题\n" + strings.Repeat("原始内容\n", 50)
	for _, limit := range []int{1, 101} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			executor := deepSeekFetchOutputFixture(t, markdown, "标题")
			profile := executor.cfg.WebFetch.Profiles["general"]
			profile.MaxMarkdownChars = limit
			executor.cfg.WebFetch.Profiles["general"] = profile
			result, preview := deepSeekFetchOutputInvoke(t, executor, map[string]any{"url": "https://example.com/"}, deepSeekFetchOutputContext(t.TempDir()))
			if result.Structured["result"] != markdown || len(contracts.AnyStringNode(preview["result"])) > limit || preview["contentTruncatedForLLM"] != true || preview["readbackAvailable"] != true {
				t.Fatalf("small configured limit must only constrain the preview: %#v", preview)
			}
		})
	}
}

func TestDeepSeekWebFetchPreviewBoundsMetadataAndPreservesSourceURL(t *testing.T) {
	markdown := "# 文档\n\n完整的简短正文。"
	title := strings.Repeat("<>&\n\"很长的标题", 5000)
	prompt := strings.Repeat("<>&\n\"很长的提取要求", 5000)
	for _, source := range []string{"https://example.com/document?a=1&b=2", "https://example.com/" + strings.Repeat("a", 6000)} {
		t.Run(source[:24], func(t *testing.T) {
			executor := deepSeekFetchOutputFixture(t, markdown, title)
			result, preview := deepSeekFetchOutputInvoke(t, executor, map[string]any{"url": source, "prompt": prompt}, deepSeekFetchOutputContext(t.TempDir()))
			if result.Structured["result"] != markdown || result.Structured["title"] != title || result.Structured["extractionPrompt"] != prompt || result.Structured["url"] != source {
				t.Fatal("model preview must not rewrite original evidence")
			}
			if preview["metadataTruncatedForLLM"] != true {
				t.Fatal("metadata truncation must be explicit")
			}
			for _, key := range []string{"title", "extractionPrompt"} {
				encoded, _ := json.Marshal(preview[key])
				if len(encoded) > 2048 {
					t.Fatalf("%s exceeds its metadata budget: %d bytes", key, len(encoded))
				}
			}
			if len(source) < 4096 {
				if preview["url"] != source {
					t.Fatal("source link was changed")
				}
			} else if _, exists := preview["url"]; exists || preview["urlOmittedForLLM"] != true || !strings.Contains(contracts.AnyStringNode(preview["urlNotice"]), "call arguments") {
				t.Fatal("oversized URL must be omitted with a pointer to original arguments, never shortened")
			}
		})
	}
}

func TestDeepSeekWebFetchUnavailableReadbackDoesNotWriteOrExpandPreview(t *testing.T) {
	markdown := strings.Repeat("一行完整资料\n", 12000)
	for _, name := range []string{"read-only", "missing-chat", "save-failed"} {
		t.Run(name, func(t *testing.T) {
			executor := deepSeekFetchOutputFixture(t, markdown, "资料")
			chatDir := t.TempDir()
			execCtx := deepSeekFetchOutputContext(chatDir)
			reason := ""
			switch name {
			case "read-only":
				execCtx.ToolExecutionPolicy = contracts.ToolExecutionPolicyReadOnly
				reason = "read_only_run"
			case "missing-chat":
				execCtx.Session.RuntimeContext.LocalPaths.ChatDir = ""
				reason = "chat_context_unavailable"
			case "save-failed":
				// A file at .tools gives a deterministic failure on Windows and Unix.
				if err := os.WriteFile(filepath.Join(chatDir, chat.ToolRootDirName), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				reason = "save_failed"
			}
			result, preview := deepSeekFetchOutputInvoke(t, executor, map[string]any{"url": "https://example.com/"}, execCtx)
			if result.Structured["result"] != markdown || preview["contentTruncatedForLLM"] != true || preview["readbackAvailable"] != false || preview["readbackUnavailableReason"] != reason {
				t.Fatalf("expected bounded preview and explicit unavailable readback: %#v", preview)
			}
			if _, exists := preview["resultRef"]; exists {
				t.Fatal("unavailable readback must not invent a file reference")
			}
			if name != "save-failed" {
				if _, err := os.Stat(filepath.Join(chatDir, chat.ToolRootDirName)); !os.IsNotExist(err) {
					t.Fatalf("preview unexpectedly wrote tool files: %v", err)
				}
			} else if data, err := os.ReadFile(filepath.Join(chatDir, chat.ToolRootDirName)); err != nil || string(data) != "keep" {
				t.Fatalf("failed save changed existing data: %v", err)
			}
		})
	}
}

func TestDeepSeekWebFetchReadbackRejectsDirectorySymlinkEscape(t *testing.T) {
	markdown := strings.Repeat("完整原始资料\n", 12000)
	for _, name := range []string{"tools-directory", "web-fetch-directory"} {
		t.Run(name, func(t *testing.T) {
			chatDir, outside := t.TempDir(), t.TempDir()
			const existing = "keep existing content"
			if err := os.WriteFile(filepath.Join(outside, "existing.md"), []byte(existing), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(chatDir, chat.ToolRootDirName)
			if name == "web-fetch-directory" {
				if err := os.Mkdir(link, 0700); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(link, "web-fetch")
			}
			if err := os.Symlink(outside, link); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("Windows directory symlink creation requires privileges: %v", err)
				}
				t.Fatal(err)
			}
			executor := deepSeekFetchOutputFixture(t, markdown, "资料")
			result, preview := deepSeekFetchOutputInvoke(t, executor, map[string]any{"url": "https://example.com/"}, deepSeekFetchOutputContext(chatDir))
			if result.Structured["result"] != markdown || preview["readbackAvailable"] != false || preview["readbackUnavailableReason"] != "save_failed" {
				t.Fatalf("escaped storage must fail with a bounded preview: %#v", preview)
			}
			if _, exists := result.Structured["resultRef"]; exists {
				t.Fatal("failed persistence must not return a file reference")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 || entries[0].Name() != "existing.md" {
				t.Fatalf("web fetch wrote outside Chat: entries=%v err=%v", entries, err)
			}
			stored, err := os.ReadFile(filepath.Join(outside, "existing.md"))
			if err != nil || string(stored) != existing {
				t.Fatalf("existing external file changed: %v", err)
			}
		})
	}
}

func TestPersistWebFetchBinaryConcurrentWritesHaveDistinctFiles(t *testing.T) {
	chatDir := t.TempDir()
	execCtx := deepSeekFetchOutputContext(chatDir)
	data := []byte("# Identical content\n")
	const count = 12
	paths := make([]string, count)
	failures := make([]error, count)
	var workers sync.WaitGroup
	for index := range paths {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			paths[index], failures[index] = persistWebFetchBinary(data, "text/markdown", execCtx)
		}(index)
	}
	workers.Wait()
	seen := map[string]bool{}
	for index, path := range paths {
		if failures[index] != nil {
			t.Fatal(failures[index])
		}
		if seen[path] {
			t.Fatalf("concurrent fetches reused %q", path)
		}
		seen[path] = true
		stored, err := os.ReadFile(path)
		if err != nil || string(stored) != string(data) {
			t.Fatalf("incomplete persisted Markdown: %v", err)
		}
	}
}
