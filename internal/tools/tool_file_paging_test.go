package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// pagingToolExecutor uses production-sized read/write limits so the model
// budget, not the small fixture limits, decides page boundaries.
func pagingToolExecutor(root string) *RuntimeToolExecutor {
	executor := fileToolExecutor(root, false)
	executor.cfg.FileTools.MaxReadBytes = 1 << 20
	executor.cfg.FileTools.MaxWriteBytes = 1 << 20
	return executor
}

func invokeEditForTest(t *testing.T, executor *RuntimeToolExecutor, execCtx *contracts.ExecutionContext, path string, oldString string, newString string, replaceAll bool) contracts.ToolExecutionResult {
	t.Helper()
	result, err := executor.invokeEdit(context.Background(), map[string]any{
		"filePath":    path,
		"oldString":   oldString,
		"newString":   newString,
		"replaceAll":  replaceAll,
		"description": "edit for test",
	}, execCtx)
	if err != nil {
		t.Fatalf("invokeEdit: %v", err)
	}
	return result
}

func readPageForTest(t *testing.T, executor *RuntimeToolExecutor, execCtx *contracts.ExecutionContext, path string, offset int) contracts.ToolExecutionResult {
	t.Helper()
	args := map[string]any{"filePath": path}
	if offset > 0 {
		args["offset"] = float64(offset)
	}
	result, err := executor.invokeRead(args, execCtx)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("invokeRead offset=%d: %#v %v", offset, result.Structured, err)
	}
	return result
}

// assertLineRanges compares a tool payload range list with "start-end" specs.
func assertLineRanges(t *testing.T, value any, want ...string) {
	t.Helper()
	items, _ := value.([]any)
	got := make([]string, 0, len(items))
	for _, item := range items {
		entry, _ := item.(map[string]any)
		got = append(got, fmt.Sprintf("%v-%v", entry["start"], entry["end"]))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ranges=%v want %v", got, want)
	}
}

// pagedFixture writes lines whose total exceeds one page and returns them.
func pagedFixture(t *testing.T, path string, lineCount int) []string {
	t.Helper()
	lines := make([]string, lineCount)
	var builder strings.Builder
	for index := range lines {
		lines[index] = fmt.Sprintf("line-%05d %s\n", index+1, strings.Repeat("x", 90))
		builder.WriteString(lines[index])
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return lines
}

func TestInvokeReadPagesLargeFileWithinModelBudget(t *testing.T) {
	root := t.TempDir()
	lines := pagedFixture(t, filepath.Join(root, "big.txt"), 1000)
	executor := pagingToolExecutor(root)
	execCtx := fileToolExecutionContext(root)

	var rebuilt strings.Builder
	offset := 0
	pages := 0
	for {
		page := readPageForTest(t, executor, execCtx, "big.txt", offset)
		pages++
		content := page.Structured["content"].(string)
		if contracts.ModelJSONStringLen(content) > contracts.ToolModelOutputMaxBytes {
			t.Fatalf("page %d exceeds model budget: %d", pages, len(content))
		}
		if page.Structured["totalLines"] != 1000 {
			t.Fatalf("unexpected totalLines %#v", page.Structured["totalLines"])
		}
		output := contracts.CompactToolModelOutput(page.Structured, "")
		if strings.Contains(output, "contentTruncatedForLLM") {
			t.Fatalf("page %d was cut again by the model output layer", pages)
		}
		rebuilt.WriteString(content)
		if page.Structured["hasMore"] != true {
			if page.Structured["endLine"] != 1000 || page.Structured["nextOffset"] != nil {
				t.Fatalf("unexpected last page %#v", page.Structured)
			}
			break
		}
		next := page.Structured["nextOffset"].(int)
		if next != page.Structured["endLine"].(int)+1 {
			t.Fatalf("nextOffset %d does not follow endLine %v", next, page.Structured["endLine"])
		}
		offset = next
	}
	if pages < 2 {
		t.Fatalf("expected several pages, got %d", pages)
	}
	if rebuilt.String() != strings.Join(lines, "") {
		t.Fatal("pages do not reassemble the file")
	}

	// Every page read for the same version together permits a full overwrite.
	written, err := executor.invokeWrite(context.Background(), map[string]any{
		"filePath":    "big.txt",
		"content":     "replaced\n",
		"description": "overwrite after paging",
	}, execCtx)
	if err != nil || written.ExitCode != 0 {
		t.Fatalf("write after reading every page: %#v %v", written.Structured, err)
	}
}

func TestInvokeEditUsesPageCoverageAndShiftsItAfterEdit(t *testing.T) {
	root := t.TempDir()
	lines := pagedFixture(t, filepath.Join(root, "big.txt"), 1000)
	executor := pagingToolExecutor(root)
	execCtx := fileToolExecutionContext(root)

	first := readPageForTest(t, executor, execCtx, "big.txt", 0)
	endLine := first.Structured["endLine"].(int)
	late := strings.TrimSuffix(lines[900], "\n")

	unseen := invokeEditForTest(t, executor, execCtx, "big.txt", late, "late-edited", false)
	if unseen.Structured["error"] != "file_edit_unseen_region" {
		t.Fatalf("expected unseen-region rejection, got %#v", unseen.Structured)
	}
	assertLineRanges(t, unseen.Structured["unseenRanges"], "901-901")

	// Insert two lines on the first page; the read lines after it shift down.
	if result := invokeEditForTest(t, executor, execCtx, "big.txt", "line-00002 ", "inserted-a\ninserted-b\nline-00002 ", false); result.ExitCode != 0 {
		t.Fatalf("edit inside first page: %#v", result.Structured)
	}
	lastSeen := strings.TrimSuffix(lines[endLine-1], "\n")
	if result := invokeEditForTest(t, executor, execCtx, "big.txt", lastSeen, "last-seen-edited", false); result.ExitCode != 0 {
		t.Fatalf("edit last line of shifted first page: %#v", result.Structured)
	}
	firstUnseen := strings.TrimSuffix(lines[endLine], "\n")
	result := invokeEditForTest(t, executor, execCtx, "big.txt", firstUnseen, "never-read", false)
	if result.Structured["error"] != "file_edit_unseen_region" {
		t.Fatalf("expected first unread line to stay unread after shift, got %#v", result.Structured)
	}
	assertLineRanges(t, result.Structured["unseenRanges"], fmt.Sprintf("%d-%d", endLine+3, endLine+3))

	// The proof was renewed for the edited version, so reading the next page
	// accumulates with it and unlocks the line.
	readPageForTest(t, executor, execCtx, "big.txt", endLine+3)
	if result := invokeEditForTest(t, executor, execCtx, "big.txt", firstUnseen, "now-read", false); result.ExitCode != 0 {
		t.Fatalf("edit after reading next page: %#v", result.Structured)
	}
}

func TestInvokeEditReplaceAllRequiresEveryMatchToBeRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "dup.txt"), []byte("token\nmiddle\ntoken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := pagingToolExecutor(root)
	execCtx := fileToolExecutionContext(root)
	if _, err := executor.invokeRead(map[string]any{"filePath": "dup.txt", "limit": float64(2)}, execCtx); err != nil {
		t.Fatal(err)
	}
	result := invokeEditForTest(t, executor, execCtx, "dup.txt", "token", "value", true)
	if result.Structured["error"] != "file_edit_unseen_region" {
		t.Fatalf("expected replaceAll rejection, got %#v", result.Structured)
	}
	assertLineRanges(t, result.Structured["matchRanges"], "1-1", "3-3")
	assertLineRanges(t, result.Structured["unseenRanges"], "3-3")
}

func TestInvokeReadLongLineAndOffsetBeyondEnd(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("中", contracts.ToolModelOutputMaxBytes) + "\n"
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(long+"short\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := pagingToolExecutor(root)
	execCtx := fileToolExecutionContext(root)

	first := readPageForTest(t, executor, execCtx, "long.txt", 0)
	content := first.Structured["content"].(string)
	if first.Structured["longLineTruncated"] != 1 || first.Structured["nextOffset"] != 2 || first.Structured["endLine"] != 0 {
		t.Fatalf("unexpected long line page %#v", first.Structured)
	}
	if content == "" || contracts.ModelJSONStringLen(content) > contracts.ToolModelOutputMaxBytes || !strings.HasPrefix(long, content) {
		t.Fatalf("long line prefix not bounded on a rune boundary: %d bytes", len(content))
	}
	if second := readPageForTest(t, executor, execCtx, "long.txt", 2); second.Structured["content"] != "short\n" || second.Structured["hasMore"] != false {
		t.Fatalf("unexpected second page %#v", second.Structured)
	}
	beyond := readPageForTest(t, executor, execCtx, "long.txt", 9)
	if beyond.Structured["content"] != "" || !strings.Contains(fmt.Sprint(beyond.Structured["message"]), "beyond the end") {
		t.Fatalf("unexpected beyond-end page %#v", beyond.Structured)
	}
}

func TestInvokeReadPagesCRLFAndGB18030(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "crlf.txt"), []byte("one\r\ntwo\r\nthree\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	encoded, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("第一行\n第二行\n第三行\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gbk.txt"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	executor := pagingToolExecutor(root)
	execCtx := fileToolExecutionContext(root)

	crlf, _ := executor.invokeRead(map[string]any{"filePath": "crlf.txt", "offset": float64(2), "limit": float64(1)}, execCtx)
	if crlf.Structured["content"] != "two\r\n" || crlf.Structured["totalLines"] != 3 {
		t.Fatalf("unexpected CRLF page %#v", crlf.Structured)
	}
	if result := invokeEditForTest(t, executor, execCtx, "crlf.txt", "two\n", "dos\n", false); result.ExitCode != 0 {
		t.Fatalf("edit CRLF line read through a page: %#v", result.Structured)
	}

	gbk, _ := executor.invokeRead(map[string]any{"filePath": "gbk.txt", "offset": float64(3)}, execCtx)
	if gbk.Structured["content"] != "第三行\n" || gbk.Structured["encoding"] != "gb18030" {
		t.Fatalf("unexpected GB18030 page %#v", gbk.Structured)
	}
}

func TestInvokeReadModelOutputKeepsMarkupUnescaped(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "page.html"), []byte("<div a=\"1\">&amp;</div>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor := pagingToolExecutor(root)
	result, err := executor.Invoke(context.Background(), "file_read", map[string]any{"filePath": "page.html"}, fileToolExecutionContext(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Output, `\u003c`) || !strings.Contains(result.Output, `<div a=\"1\">&amp;</div>`) {
		t.Fatalf("expected unescaped markup in model output, got %s", result.Output)
	}
}

func TestChatFileVersionLedgerMergesPagesAcrossRunsAndIgnoresV1(t *testing.T) {
	root := t.TempDir()
	pagedFixture(t, filepath.Join(root, "big.txt"), 1000)
	executor := pagingToolExecutor(root)
	store, err := chat.NewFileStoreAtStartup(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	executor.chats = store
	executor.cfg.FileTools.ReadBeforeWriteScope = "chat"
	runCtx := func(runID string) *contracts.ExecutionContext {
		ctx := fileToolExecutionContext(root)
		ctx.Session.ChatID = "chat-pages"
		ctx.Session.RunID = runID
		return ctx
	}

	first := runCtx("run-1")
	page := readPageForTest(t, executor, first, "big.txt", 0)
	next := page.Structured["nextOffset"].(int)

	// A later Run reads the remaining pages; the ledger accumulates them.
	second := runCtx("run-2")
	for offset := next; offset > 0; {
		result := readPageForTest(t, executor, second, "big.txt", offset)
		offset, _ = result.Structured["nextOffset"].(int)
	}
	third := runCtx("run-3")
	written, err := executor.invokeWrite(context.Background(), map[string]any{"filePath": "big.txt", "content": "x\n", "description": "overwrite"}, third)
	if err != nil || written.ExitCode != 0 {
		t.Fatalf("write with pages from two runs: %#v %v", written.Structured, err)
	}

	ledgerPath, _ := executor.chatFileVersionsPath(third)
	data, err := os.ReadFile(ledgerPath)
	if err != nil || !bytes.Contains(data, []byte(`"version": 2`)) {
		t.Fatalf("expected v2 ledger, got %s %v", data, err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["version"] = 1
	legacyData, _ := json.Marshal(legacy)
	if err := os.WriteFile(ledgerPath, legacyData, 0o644); err != nil {
		t.Fatal(err)
	}
	result := invokeEditForTest(t, executor, runCtx("run-4"), "big.txt", "x", "y", false)
	if result.Structured["error"] != "file_edit_not_read" {
		t.Fatalf("expected v1 ledger to be ignored, got %#v", result.Structured)
	}
}
