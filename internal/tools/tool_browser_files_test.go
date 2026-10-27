package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
)

func browserFilesTestRuntime(t *testing.T, respond func(ClientRequest) map[string]any) (*RuntimeToolExecutor, *ExecutionContext, *webControlTestInvoker, string) {
	t.Helper()
	executor, execCtx, invoker := webControlTestRuntime(t, respond)
	executor.cfg.Paths.ChatsDir = filepath.Join(t.TempDir(), "chats")
	level := executor.cfg.AccessPolicy.Levels[AccessLevelDefault]
	level.WriteRoots = []string{"@chat"}
	executor.cfg.AccessPolicy.Levels[AccessLevelDefault] = level
	chatDir := filepath.Join(executor.cfg.Paths.ChatsDir, execCtx.Session.ChatID)
	if err := os.MkdirAll(chatDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return executor, execCtx, invoker, chatDir
}

func browserUploadArgs() map[string]any {
	return map[string]any{"surfaceId": "page:chrome-1", "observationId": "o1", "ref": "file1", "referenceNames": []any{"report.txt"}}
}

func TestBrowserFilesUploadCurrentChatReferencesPrivately(t *testing.T) {
	for _, chooser := range []bool{false, true} {
		executor, execCtx, invoker, chatDir := browserFilesTestRuntime(t, func(request ClientRequest) map[string]any {
			params := request.Payload["params"].(map[string]any)
			files := params["files"].([]any)
			if request.Payload["method"] != "Surface.uploadFiles" || params["observationId"] != "o1" || len(files) != 2 {
				t.Fatal("wrong upload contract")
			}
			for index, raw := range files {
				file := raw.(map[string]any)
				data, err := base64.StdEncoding.DecodeString(file["data"].(string))
				if err != nil || string(data) != []string{"private report", "column\nvalue\n"}[index] || file["mimeType"] == "" || len(file) != 3 {
					t.Fatalf("unexpected private wire file: index=%d", index)
				}
			}
			// Even a host accidentally echoing wire files cannot expose bytes.
			return map[string]any{"ok": true, "result": map[string]any{"outcome": "completed", "files": files, "observation": map[string]any{"observationId": "o2"}}}
		})
		mustWriteFile(t, filepath.Join(chatDir, "report.txt"), "private report")
		if err := os.MkdirAll(filepath.Join(chatDir, "artifacts", "r1"), 0o700); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, filepath.Join(chatDir, "artifacts", "r1", "table.csv"), "column\nvalue\n")
		args := browserUploadArgs()
		args["referenceNames"] = []any{"report.txt", "@chat/artifacts/r1/table.csv"}
		if chooser {
			delete(args, "ref")
			args["chooserId"] = "chooser1"
		}
		result, err := executor.Invoke(context.Background(), "surface_upload", args, execCtx)
		if err != nil || result.ExitCode != 0 || len(invoker.requests) != 1 {
			t.Fatalf("upload: %#v %v", result, err)
		}
		for _, forbidden := range []string{"private report", base64.StdEncoding.EncodeToString([]byte("private report")), chatDir, `"data":`} {
			if strings.Contains(result.Output, forbidden) {
				t.Fatalf("upload leaked file data/path: %q", forbidden)
			}
		}
		params := invoker.requests[0].Payload["params"].(map[string]any)
		field := "ref"
		if chooser {
			field = "chooserId"
		}
		if params[field] != args[field] {
			t.Fatalf("lost upload target %s", field)
		}
		source := invoker.requests[0].Payload["source"].(map[string]any)
		if source["chatId"] != execCtx.Session.ChatID || source["runId"] != execCtx.Session.RunID {
			t.Fatal("upload source was not trusted")
		}
	}
}

func TestBrowserFilesUploadRejectsScopeAndSizeBeforeDispatch(t *testing.T) {
	executor, execCtx, invoker, chatDir := browserFilesTestRuntime(t, nil)
	mustWriteFile(t, filepath.Join(chatDir, "report.txt"), "report")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	mustWriteFile(t, outside, "private outside")
	if err := os.Symlink(outside, filepath.Join(chatDir, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(chatDir, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{outside, "../another-chat/file.txt", "@workspace/report.txt", `C:\secret.txt`, "@chat/../escape.txt", ".tools/secret.json", ".TOOLS/secret.json", ".BtW/private.jsonl", ".uploads.jsonl", "escape.txt", "folder", "missing.txt"} {
		args := browserUploadArgs()
		args["referenceNames"] = []any{name}
		result, err := executor.Invoke(context.Background(), "surface_upload", args, execCtx)
		if err != nil || result.ExitCode != -1 || result.Error == "" {
			t.Fatalf("accepted invalid reference %q: %#v %v", name, result, err)
		}
		if strings.Contains(result.Output, outside) || strings.Contains(result.Output, chatDir) || strings.Contains(result.Output, "private outside") {
			t.Fatal("file error leaked host path or data")
		}
	}
	large := filepath.Join(chatDir, "large.bin")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(browserFileMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	for _, names := range []any{[]any{}, []any{"large.bin"}, []any{"report.txt", "report.txt", "report.txt", "report.txt", "report.txt", "report.txt", "report.txt", "report.txt", "report.txt"}} {
		args := browserUploadArgs()
		args["referenceNames"] = names
		result, err := executor.Invoke(context.Background(), "surface_upload", args, execCtx)
		if err != nil || result.ExitCode != -1 {
			t.Fatalf("invalid file count/size accepted: %#v %v", result, err)
		}
	}
	file, err = os.Create(filepath.Join(chatDir, "batch.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(3 << 20); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	argsForBatch := browserUploadArgs()
	argsForBatch["referenceNames"] = []any{"batch.bin", "batch.bin"}
	batchResult, err := executor.Invoke(context.Background(), "surface_upload", argsForBatch, execCtx)
	if err != nil || batchResult.Error != "surface_upload_too_large" {
		t.Fatalf("aggregate upload limit not enforced: %#v %v", batchResult, err)
	}
	for _, field := range []string{"source", "files", "path", "profile"} {
		args := browserUploadArgs()
		args[field] = "forged"
		result, _ := executor.Invoke(context.Background(), "surface_upload", args, execCtx)
		if result.Error != "invalid_args" {
			t.Fatalf("accepted forged field %s", field)
		}
	}
	args := browserUploadArgs()
	args["chooserId"] = "chooser1"
	result, _ := executor.Invoke(context.Background(), "surface_upload", args, execCtx)
	if result.Error != "invalid_args" {
		t.Fatal("accepted ambiguous upload target")
	}
	if len(invoker.requests) != 0 {
		t.Fatalf("rejected file reached Desktop: %d", len(invoker.requests))
	}
}

func TestBrowserFilesMaximumUploadFitsExistingDesktopFrameLimit(t *testing.T) {
	executor, execCtx, invoker, chatDir := browserFilesTestRuntime(t, nil)
	file, err := os.Create(filepath.Join(chatDir, "report.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(browserFileMaxBytes); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	result, err := executor.Invoke(context.Background(), "surface_upload", browserUploadArgs(), execCtx)
	if err != nil || result.ExitCode != 0 || len(invoker.requests) != 1 {
		t.Fatalf("4 MiB upload rejected: %#v %v", result, err)
	}
	encoded, err := json.Marshal(invoker.requests[0])
	if err != nil || len(encoded) >= 8<<20 {
		t.Fatalf("upload exceeds Desktop 8 MiB incoming frame contract: %d %v", len(encoded), err)
	}
}

func browserDownloadWire(data []byte) map[string]any {
	sum := sha256.Sum256(data)
	return map[string]any{"downloadId": "download1", "fileName": "report.txt", "mimeType": "text/plain", "data": base64.StdEncoding.EncodeToString(data), "sizeBytes": len(data), "sha256": hex.EncodeToString(sum[:])}
}

func browserSavedMetadata(t *testing.T, result ToolExecutionResult) map[string]any {
	t.Helper()
	response, ok := result.Structured["response"].(map[string]any)
	if !ok {
		t.Fatalf("missing response: %#v", result)
	}
	node, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %#v", result)
	}
	return node
}

func TestBrowserFilesDownloadPublishesPrivateBytesAtomicallyAndIdempotently(t *testing.T) {
	data := []byte("private download content")
	var chatDir string
	acknowledgements := 0
	executor, execCtx, invoker, chatDir := browserFilesTestRuntime(t, func(request ClientRequest) map[string]any {
		switch request.Payload["method"] {
		case "Surface.readDownload":
			return map[string]any{"ok": true, "result": browserDownloadWire(data)}
		case "Surface.releaseDownload":
			params := request.Payload["params"].(map[string]any)
			metadata, err := executorDownloadReceiptForTest(chatDir, "page:chrome-1", "download1")
			if err != nil || params["downloadId"] != "download1" || params["sha256"] != metadata.SHA256 || len(params) != 2 {
				t.Fatalf("cleanup preceded durable publication or used wrong identity: %#v %v", params, err)
			}
			acknowledgements++
			if acknowledgements > 1 {
				return map[string]any{"ok": true, "result": map[string]any{"released": true}}
			}
			return map[string]any{"ok": true, "result": map[string]any{"released": true, "downloadId": "download1", "sha256": metadata.SHA256}}
		default:
			t.Fatalf("unexpected download method: %s", request.Payload["method"])
		}
		return nil
	})
	args := map[string]any{"surfaceId": "page:chrome-1", "action": "save", "downloadId": "download1"}
	var name string
	for i := 0; i < 2; i++ {
		result, err := executor.Invoke(context.Background(), "surface_downloads", args, execCtx)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("save: %#v %v", result, err)
		}
		metadata := browserSavedMetadata(t, result)
		if metadata["saved"] != true || metadata["dataOmitted"] != true || metadata["fileName"] != "report.txt" || metadata["cleanupPending"] != nil {
			t.Fatalf("missing saved metadata: %#v", metadata)
		}
		if i == 0 {
			name = metadata["referenceName"].(string)
		} else if metadata["referenceName"] != name {
			t.Fatal("repeat save changed reference")
		}
		for _, forbidden := range []string{string(data), base64.StdEncoding.EncodeToString(data), chatDir, `"data":`, `"filePath":`} {
			if strings.Contains(result.Output, forbidden) {
				t.Fatalf("download result leaked %q", forbidden)
			}
		}
		if !strings.HasPrefix(metadata["url"].(string), "@chat/") {
			t.Fatal("missing Chat reference")
		}
	}
	path := filepath.Join(chatDir, name)
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != string(data) {
		t.Fatalf("saved content mismatch: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("download permissions: %v %v", info, err)
	}
	_, err = executor.persistBrowserDownload(context.Background(), "page:chrome-1", "download1", "report.txt", "text/plain", []byte("different content for same id"), execCtx)
	if !errors.Is(err, errBrowserDownloadConflict) {
		t.Fatalf("same-id conflict not preserved: %v", err)
	}
	savedAgain, _ := os.ReadFile(path)
	if string(savedAgain) != string(saved) {
		t.Fatal("conflict overwrote existing file")
	}
	if len(invoker.requests) != 3 || invoker.requests[0].Payload["method"] != "Surface.readDownload" || invoker.requests[1].Payload["method"] != "Surface.releaseDownload" || invoker.requests[2].Payload["method"] != "Surface.releaseDownload" {
		t.Fatalf("repeat save must not reread the released browser file: %#v", invoker.requests)
	}
}

func executorDownloadReceiptForTest(chatDir, surfaceID, downloadID string) (browserDownloadReceipt, error) {
	_, receiptPath := browserDownloadReceiptLocation(surfaceID, downloadID)
	data, err := os.ReadFile(filepath.Join(chatDir, receiptPath))
	var receipt browserDownloadReceipt
	if err == nil {
		err = json.Unmarshal(data, &receipt)
	}
	return receipt, err
}

func TestBrowserFilesSavedDownloadSurvivesCleanupFailureWithoutReread(t *testing.T) {
	data := []byte("durable Chat file")
	for _, ack := range []map[string]any{
		{"ok": false, "error": map[string]any{"code": "download_not_found", "message": "private worker path"}},
		{"ok": true, "result": map[string]any{"released": false, "data": "private worker bytes"}},
		{"ok": true, "result": map[string]any{"released": true, "downloadId": "another-download"}},
		{"ok": true, "result": map[string]any{"released": true, "sha256": strings.Repeat("0", 64)}},
	} {
		executor, execCtx, invoker, chatDir := browserFilesTestRuntime(t, func(request ClientRequest) map[string]any {
			if request.Payload["method"] == "Surface.readDownload" {
				return map[string]any{"ok": true, "result": browserDownloadWire(data)}
			}
			return ack
		})
		args := map[string]any{"surfaceId": "p", "action": "save", "downloadId": "download1"}
		for range 2 {
			result, err := executor.Invoke(context.Background(), "surface_downloads", args, execCtx)
			metadata := browserSavedMetadata(t, result)
			if err != nil || result.ExitCode != 0 || metadata["saved"] != true || metadata["cleanupPending"] != true || strings.Contains(result.Output, "private worker") || strings.Contains(result.Output, string(data)) {
				t.Fatalf("cleanup failure lost or leaked a durable save: %#v %v", result, err)
			}
		}
		if len(invoker.requests) != 3 || invoker.requests[2].Payload["method"] != "Surface.releaseDownload" {
			t.Fatalf("cleanup retry reread or replayed the download: %#v", invoker.requests)
		}
		receipt, err := executorDownloadReceiptForTest(chatDir, "p", "download1")
		if err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, filepath.Join(chatDir, receipt.ReferenceName), "changed Chat file")
		result, err := executor.Invoke(context.Background(), "surface_downloads", args, execCtx)
		if err != nil || result.ExitCode == 0 || len(invoker.requests) != 3 {
			t.Fatalf("changed published file was acknowledged or refetched: %#v %v", result, err)
		}
	}
}

func TestBrowserFilesSavedDownloadSurvivesCleanupTransportFailure(t *testing.T) {
	executor, execCtx, _, _ := browserFilesTestRuntime(t, nil)
	metadata, err := executor.persistBrowserDownload(context.Background(), "p", "download1", "report.txt", "text/plain", []byte("already durable"), execCtx)
	if err != nil {
		t.Fatal(err)
	}
	for _, transportErr := range []error{context.Canceled, context.DeadlineExceeded, errors.New("private transport path")} {
		invoker := &scriptedClientRequestInvoker{err: transportErr}
		executor.clientRequest = invoker
		result, err := executor.Invoke(context.Background(), "surface_downloads", map[string]any{"surfaceId": "p", "action": "save", "downloadId": "download1"}, execCtx)
		saved := browserSavedMetadata(t, result)
		if err != nil || result.ExitCode != 0 || saved["saved"] != true || saved["cleanupPending"] != true || saved["referenceName"] != metadata["referenceName"] || invoker.calls != 1 || invoker.request.Payload["method"] != "Surface.releaseDownload" || strings.Contains(result.Output, "private transport") {
			t.Fatalf("cleanup transport failure invalidated or replayed the save: %#v %v", result, err)
		}
	}
}

func TestBrowserFilesSavedDownloadRejectsInvalidReceiptBeforeCleanup(t *testing.T) {
	for _, mutate := range []func(*browserDownloadReceipt){
		func(r *browserDownloadReceipt) { r.ReferenceName = "../private-outside.txt" },
		func(r *browserDownloadReceipt) { r.FileName = "another-file.txt" },
		func(r *browserDownloadReceipt) { r.SizeBytes = browserFileMaxBytes + 1 },
		func(r *browserDownloadReceipt) { r.SHA256 = strings.Repeat("0", 64) },
		func(r *browserDownloadReceipt) { r.MimeType = "invalid MIME" },
	} {
		executor, execCtx, invoker, chatDir := browserFilesTestRuntime(t, nil)
		_, err := executor.persistBrowserDownload(context.Background(), "p", "download1", "report.txt", "text/plain", []byte("durable file"), execCtx)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := executorDownloadReceiptForTest(chatDir, "p", "download1")
		if err != nil {
			t.Fatal(err)
		}
		mutate(&receipt)
		_, path := browserDownloadReceiptLocation("p", "download1")
		data, _ := json.Marshal(receipt)
		if err := os.WriteFile(filepath.Join(chatDir, path), data, 0o600); err != nil {
			t.Fatal(err)
		}
		result, err := executor.Invoke(context.Background(), "surface_downloads", map[string]any{"surfaceId": "p", "action": "save", "downloadId": "download1"}, execCtx)
		if err != nil || result.ExitCode == 0 || len(invoker.requests) != 0 || strings.Contains(result.Output, "private-outside") || strings.Contains(result.Output, `"saved":true`) {
			t.Fatalf("invalid receipt was released, refetched or exposed: %#v %v", result, err)
		}
	}
}

func TestBrowserFilesDownloadValidatesIntegrityAndDoesNotClaimFailedSaves(t *testing.T) {
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { m["sha256"] = strings.Repeat("0", 64) },
		func(m map[string]any) { m["sizeBytes"] = 100 },
		func(m map[string]any) { m["data"] = "invalid!base64" },
		func(m map[string]any) { m["downloadId"] = "another-page-download" },
		func(m map[string]any) { m["sizeBytes"] = browserFileMaxBytes + 1 },
	} {
		executor, execCtx, invoker, chatDir := browserFilesTestRuntime(t, func(ClientRequest) map[string]any {
			node := browserDownloadWire([]byte("secret bytes"))
			mutate(node)
			return map[string]any{"ok": true, "result": node}
		})
		result, err := executor.Invoke(context.Background(), "surface_downloads", map[string]any{"surfaceId": "p", "action": "save", "downloadId": "download1"}, execCtx)
		if err != nil || result.ExitCode != -1 || strings.Contains(result.Output, `"saved":true`) || strings.Contains(result.Output, "secret bytes") || strings.Contains(result.Output, chatDir) {
			t.Fatalf("invalid download exposed/saved: %#v %v", result, err)
		}
		entries, _ := os.ReadDir(chatDir)
		if len(entries) != 0 {
			t.Fatal("invalid download wrote files")
		}
		if len(invoker.requests) != 1 || invoker.requests[0].Payload["method"] != "Surface.readDownload" {
			t.Fatal("invalid download was acknowledged")
		}
	}
	executor, execCtx, _, chatDir := browserFilesTestRuntime(t, func(ClientRequest) map[string]any {
		return map[string]any{"ok": true, "result": browserDownloadWire([]byte("secret bytes"))}
	})
	mustWriteFile(t, filepath.Join(chatDir, ".tools"), "existing file")
	result, err := executor.Invoke(context.Background(), "surface_downloads", map[string]any{"surfaceId": "p", "action": "save", "downloadId": "download1"}, execCtx)
	if err != nil || result.Error != "surface_download_save_failed" || strings.Contains(result.Output, `"saved":true`) || strings.Contains(result.Output, chatDir) {
		t.Fatalf("failed persistence claimed saved: %#v %v", result, err)
	}
}

func TestBrowserFilesDownloadListOnlyReturnsMetadata(t *testing.T) {
	executor, execCtx, invoker, _ := browserFilesTestRuntime(t, func(ClientRequest) map[string]any {
		return map[string]any{"ok": true, "result": map[string]any{"downloads": []any{map[string]any{"downloadId": "download1", "fileName": "report.txt", "status": "completed", "sizeBytes": 3, "path": "/private/secret", "url": "https://example.com/?token=private-token", "data": "private-data"}}}}
	})
	result, err := executor.Invoke(context.Background(), "surface_downloads", map[string]any{"surfaceId": "p", "action": "list"}, execCtx)
	if err != nil || result.ExitCode != 0 || invoker.requests[0].Payload["method"] != "Surface.getDownloads" {
		t.Fatalf("list: %#v %v", result, err)
	}
	for _, forbidden := range []string{"private-data", "private-token", "/private/secret", `"url":`, `"path":`} {
		if strings.Contains(result.Output, forbidden) {
			t.Fatalf("list leaked %s", forbidden)
		}
	}
}

func TestBrowserFilesDownloadListPreservesEightFilesAndOneLimitNotice(t *testing.T) {
	for _, count := range []int{8, 9, 10} {
		executor, execCtx, _, _ := browserFilesTestRuntime(t, func(ClientRequest) map[string]any {
			items := make([]any, 0, count)
			for index := 0; index < count; index++ {
				item := map[string]any{"downloadId": strings.Repeat("d", index+1), "fileName": "report.txt", "status": "complete"}
				if index >= 8 {
					item["status"] = "failed"
					item["error"] = "file_transfer_limit"
				}
				items = append(items, item)
			}
			return map[string]any{"ok": true, "result": map[string]any{"downloads": items}}
		})
		result, err := executor.Invoke(context.Background(), "surface_downloads", map[string]any{"surfaceId": "p", "action": "list"}, execCtx)
		if err != nil {
			t.Fatal(err)
		}
		if count > 9 {
			if result.Error != "surface_download_invalid_response" {
				t.Fatal("accepted more than one overflow receipt")
			}
			continue
		}
		if result.ExitCode != 0 {
			t.Fatalf("valid bounded list rejected: %#v", result)
		}
		items := browserSavedMetadata(t, result)["downloads"].([]any)
		if len(items) != count {
			t.Fatal("lost retained files or failure notice")
		}
		if count == 9 && items[8].(map[string]any)["error"] != "file_transfer_limit" {
			t.Fatal("failure notice lost error classification")
		}
	}
	for _, notice := range []map[string]any{
		{"downloadId": "ninth", "fileName": "report.txt", "status": "complete"},
		{"downloadId": "ninth", "fileName": "report.txt", "status": "failed", "error": "download_failed"},
	} {
		executor, execCtx, _, _ := browserFilesTestRuntime(t, func(ClientRequest) map[string]any {
			items := []any{}
			for index := 0; index < 8; index++ {
				items = append(items, map[string]any{"downloadId": strings.Repeat("d", index+1), "fileName": "report.txt", "status": "complete"})
			}
			items = append(items, notice)
			return map[string]any{"ok": true, "result": map[string]any{"downloads": items}}
		})
		result, err := executor.Invoke(context.Background(), "surface_downloads", map[string]any{"surfaceId": "p", "action": "list"}, execCtx)
		if err != nil || result.Error != "surface_download_invalid_response" {
			t.Fatalf("accepted ninth retained transfer: %#v %v", result, err)
		}
	}
}

func TestBrowserFilesDownloadUsesExistingChunkedResponseCollector(t *testing.T) {
	executor, execCtx, _, _ := browserFilesTestRuntime(t, nil)
	node := browserDownloadWire([]byte(strings.Repeat("streamed-private-", 70000)))
	payload, _ := json.Marshal(map[string]any{"ok": true, "method": "Surface.readDownload", "result": node})
	frames := []ClientResponseFrame{}
	for offset, seq := 0, int64(1); offset < len(payload); seq++ {
		end := offset + 192*1024
		if end > len(payload) {
			end = len(payload)
		}
		event, _ := json.Marshal(desktopStreamEvent{Seq: seq, Type: desktopResponseDeltaEventType, Timestamp: 1771888000000 + seq, Encoding: "base64", Chunk: base64.StdEncoding.EncodeToString(payload[offset:end])})
		frames = append(frames, ClientResponseFrame{Frame: "stream", StreamID: "file-stream", Event: event})
		offset = end
	}
	terminal, _ := json.Marshal(map[string]any{"streamed": true, "streamId": "file-stream", "encoding": "base64", "contentType": "application/json", "chunkCount": len(frames), "totalBytes": len(payload)})
	zero := 0
	frames = append(frames, ClientResponseFrame{Frame: "response", Type: desktopCDPRequestType, Code: &zero, Data: terminal})
	executor.clientRequest = &scriptedClientRequestInvoker{frames: frames}
	result, err := executor.Invoke(context.Background(), "surface_downloads", map[string]any{"surfaceId": "p", "action": "save", "downloadId": "download1"}, execCtx)
	if err != nil || result.ExitCode != 0 || browserSavedMetadata(t, result)["saved"] != true || strings.Contains(result.Output, "streamed-private-") {
		t.Fatalf("chunked save failed: %#v %v", result, err)
	}
}

func TestBrowserFilesStandaloneAndErrorMessagesDoNotExposePrivatePaths(t *testing.T) {
	executor, execCtx, invoker, _ := browserFilesTestRuntime(t, func(ClientRequest) map[string]any {
		return map[string]any{"ok": false, "error": map[string]any{"code": "file_unavailable", "message": "open /private/secret-path: private-data"}, "result": map[string]any{"data": "private-data"}}
	})
	result, err := executor.Invoke(context.Background(), "surface_downloads", map[string]any{"surfaceId": "p", "action": "save", "downloadId": "download1"}, execCtx)
	if err != nil || result.ExitCode != -1 || strings.Contains(result.Output, "/private/") || strings.Contains(result.Output, "private-data") {
		t.Fatalf("error leaked details: %#v %v", result, err)
	}
	executor.cfg.RuntimeMode = config.RuntimeModeStandalone
	result, err = executor.Invoke(context.Background(), "surface_upload", browserUploadArgs(), execCtx)
	if err != nil || result.Error != "desktop_cdp_unsupported_runtime" || len(invoker.requests) != 1 {
		t.Fatalf("standalone upload read/sent: %#v %v", result, err)
	}
}

func TestBrowserFilesUnknownDownloadDoesNotWriteOrExposeResponse(t *testing.T) {
	executor, execCtx, invoker, chatDir := browserFilesTestRuntime(t, func(ClientRequest) map[string]any {
		return map[string]any{"ok": false, "error": map[string]any{"code": "download_not_found", "message": "unknown download at /private/worker/path"}, "result": map[string]any{"data": "private-byte-payload"}}
	})
	result, err := executor.Invoke(context.Background(), "surface_downloads", map[string]any{"surfaceId": "p", "action": "save", "downloadId": "not-a-download"}, execCtx)
	if err != nil || result.ExitCode != -1 || !strings.Contains(result.Output, "download_not_found") || strings.Contains(result.Output, "/private/") || strings.Contains(result.Output, "private-byte-payload") || len(invoker.requests) != 1 {
		t.Fatalf("unknown download response: %#v %v", result, err)
	}
	entries, _ := os.ReadDir(chatDir)
	if len(entries) != 0 {
		t.Fatal("unknown download wrote files")
	}
}

func TestBrowserFilesUploadFailurePreservesOutcomeAndObservationWithoutBytes(t *testing.T) {
	executor, execCtx, _, chatDir := browserFilesTestRuntime(t, func(ClientRequest) map[string]any {
		return map[string]any{"ok": false, "error": map[string]any{"code": "dialog_pending", "message": "private diagnostic at /private/worker"}, "result": map[string]any{
			"outcome": "unknown", "error": "dialog_pending", "data": "private-file-bytes",
			"observation": map[string]any{"observationId": "o2", "dialogs": []any{map[string]any{"dialogId": "d1", "type": "confirm", "message": "Continue?"}}, "files": []any{map[string]any{"name": "report.txt", "data": "private-file-bytes", "path": "/private/worker"}}},
		}}
	})
	mustWriteFile(t, filepath.Join(chatDir, "report.txt"), "selected report")
	result, err := executor.Invoke(context.Background(), "surface_upload", browserUploadArgs(), execCtx)
	if err != nil || result.ExitCode != -1 {
		t.Fatalf("upload failure: %#v %v", result, err)
	}
	node := browserSavedMetadata(t, result)
	observation, _ := node["observation"].(map[string]any)
	if node["outcome"] != "unknown" || node["error"] != "dialog_pending" || observation["observationId"] != "o2" {
		t.Fatalf("upload lost reconciliation state: %#v", result)
	}
	if strings.Contains(result.Output, "private-file-bytes") || strings.Contains(result.Output, "/private/worker") || strings.Contains(result.Output, chatDir) {
		t.Fatal("upload failure leaked file data/path")
	}
}

func TestBrowserFilesDownloadSanitizesWindowsAndMultibyteFileNames(t *testing.T) {
	for _, name := range []string{"CON", "nul.txt", "COM1.csv", "lpt9", `C:\private\report.txt`, "../../report.txt", "bad?name*.txt", strings.Repeat("测", 120) + ".txt", ".."} {
		safe := browserDownloadFileName(name)
		if safe == "" || safe == "." || safe == ".." || len(safe) > 121 || strings.ContainsAny(safe, `<>:"/\|?*`) {
			t.Fatalf("unsafe file name %q -> %q", name, safe)
		}
		if (strings.EqualFold(name, "CON") || strings.EqualFold(name, "nul.txt") || strings.EqualFold(name, "COM1.csv") || strings.EqualFold(name, "lpt9")) && !strings.HasPrefix(safe, "_") {
			t.Fatalf("reserved name not normalized: %s", safe)
		}
	}
}

func TestBrowserFilesDownloadDoesNotFollowExistingChatSymlink(t *testing.T) {
	data := []byte("download content")
	executor, execCtx, _, chatDir := browserFilesTestRuntime(t, func(ClientRequest) map[string]any {
		return map[string]any{"ok": true, "result": browserDownloadWire(data)}
	})
	key := sha256.Sum256([]byte("p\x00download1"))
	name := "chrome-download-" + hex.EncodeToString(key[:])[:24] + "-report.txt"
	outside := filepath.Join(t.TempDir(), "preserved.txt")
	mustWriteFile(t, outside, "original outside file")
	if err := os.Symlink(outside, filepath.Join(chatDir, name)); err != nil {
		t.Fatal(err)
	}
	result, err := executor.Invoke(context.Background(), "surface_downloads", map[string]any{"surfaceId": "p", "action": "save", "downloadId": "download1"}, execCtx)
	if err != nil || result.ExitCode != -1 || strings.Contains(result.Output, `"saved":true`) || strings.Contains(result.Output, outside) {
		t.Fatalf("existing symlink followed: %#v %v", result, err)
	}
	saved, _ := os.ReadFile(outside)
	if string(saved) != "original outside file" {
		t.Fatal("download changed file outside Chat")
	}
}
