package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

const (
	browserFileMaxBytes   = 4 << 20
	browserUploadMaxFiles = 8
)

// Serialize publication of a download and its local receipt. Receipts contain
// metadata only; the worker remains the owner of in-flight browser downloads.
var browserDownloadPublication sync.Mutex

func (t *RuntimeToolExecutor) browserFilesChatDir(execCtx *ExecutionContext) (string, bool) {
	if execCtx == nil || t == nil || strings.TrimSpace(t.cfg.Paths.ChatsDir) == "" || !chat.ValidChatID(execCtx.Session.ChatID) {
		return "", false
	}
	if requestChat := strings.TrimSpace(execCtx.Request.ChatID); requestChat != "" && requestChat != execCtx.Session.ChatID {
		return "", false
	}
	return filepath.Join(t.cfg.Paths.ChatsDir, execCtx.Session.ChatID), true
}

func browserFileFailure(code, message string) ToolExecutionResult {
	// Filesystem errors can contain a private local path. Only fixed diagnostics
	// cross the tool boundary, and rejected client payloads are never echoed here.
	return desktopActionErrorResult(code, message, nil)
}

func (t *RuntimeToolExecutor) webControlSurfaceUpload(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "observationId", "ref", "chooserId", "referenceNames"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	observationID, failure, failed := webControlString(args, "observationId", true)
	if failed {
		return failure, nil
	}
	_, hasRef := args["ref"]
	_, hasChooser := args["chooserId"]
	if hasRef == hasChooser {
		return webControlInvalidArgs("provide exactly one of ref or chooserId", "ref"), nil
	}
	targetField := "ref"
	if hasChooser {
		targetField = "chooserId"
	}
	target, failure, failed := webControlString(args, targetField, true)
	if failed {
		return failure, nil
	}
	names, valid := webControlStringArray(args["referenceNames"], browserUploadMaxFiles, 4096)
	if !valid || len(names) == 0 {
		return webControlInvalidArgs("referenceNames must contain 1–8 existing current-Chat file references", "referenceNames"), nil
	}
	if t.cfg.RuntimeMode != config.RuntimeModeDesktop {
		return browserFileFailure("desktop_cdp_unsupported_runtime", "webpage file transfer requires the Desktop runtime"), nil
	}
	chatDir, valid := t.browserFilesChatDir(execCtx)
	if !valid {
		return browserFileFailure("surface_upload_chat_unavailable", "the current Chat file context is unavailable"), nil
	}
	root, err := os.OpenRoot(chatDir)
	if err != nil {
		return browserFileFailure("surface_upload_file_unavailable", "a referenced Chat file is unavailable"), nil
	}
	defer root.Close()
	session := t.policySession(execCtx)
	session.ChatRoot = chatDir
	files := make([]any, 0, len(names))
	total := 0
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return browserFileFailure("surface_upload_cancelled", "upload preparation was cancelled; no browser request was sent"), nil
		}
		relative := name
		if chat.HasChatScopeAlias(relative) {
			relative = relative[len(chat.ChatScopeAlias):]
		}
		if strings.ContainsAny(relative, `\:`) || filepath.IsAbs(relative) || strings.EqualFold(relative, ".uploads.jsonl") {
			return webControlInvalidArgs("referenceNames must stay within the current Chat; host paths are not accepted", "referenceNames"), nil
		}
		// Internal roots are private on case-insensitive macOS/Windows volumes
		// too; a casing variant must not turn a tool receipt into an attachment.
		for _, segment := range strings.Split(relative, "/") {
			if strings.EqualFold(segment, chat.ToolRootDirName) || strings.EqualFold(segment, ".btw") || strings.EqualFold(segment, ".explain") {
				return webControlInvalidArgs("referenceNames must name public files within the current Chat", "referenceNames"), nil
			}
		}
		alias, err := chat.BuildChatAliasRef(relative)
		if err != nil {
			return webControlInvalidArgs("referenceNames must name public files within the current Chat", "referenceNames"), nil
		}
		relative = strings.TrimPrefix(alias, chat.ChatScopeAlias)
		access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, session, filetools.ReadAccess, filepath.Join(chatDir, filepath.FromSlash(relative)))
		if err != nil || access.Blocked || !access.AllowedByWhitelist && !access.AutoApproved || !filetools.PathInSessionChat(session, access.Path) {
			return browserFileFailure("surface_upload_file_blocked", "the referenced file is outside the permitted current-Chat file scope"), nil
		}
		// os.Root also enforces containment at open time, including symlink
		// replacement races. A full-access Run cannot broaden this file API.
		info, err := root.Stat(relative)
		if err != nil || !info.Mode().IsRegular() {
			return browserFileFailure("surface_upload_file_unavailable", "a referenced Chat file is unavailable or is not a regular file"), nil
		}
		if info.Size() > browserFileMaxBytes || int64(total)+info.Size() > browserFileMaxBytes {
			return browserFileFailure("surface_upload_too_large", "uploads are limited to 4 MiB per file and 4 MiB in total"), nil
		}
		file, err := root.Open(relative)
		if err != nil {
			return browserFileFailure("surface_upload_file_unavailable", "a referenced Chat file could not be read"), nil
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !openedInfo.Mode().IsRegular() {
			_ = file.Close()
			return browserFileFailure("surface_upload_file_unavailable", "a referenced Chat file is not a regular file"), nil
		}
		data, readErr := io.ReadAll(io.LimitReader(file, int64(browserFileMaxBytes-total)+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return browserFileFailure("surface_upload_file_unavailable", "a referenced Chat file could not be read"), nil
		}
		total += len(data)
		if len(data) > browserFileMaxBytes || total > browserFileMaxBytes {
			return browserFileFailure("surface_upload_too_large", "uploads are limited to 4 MiB per file and 4 MiB in total"), nil
		}
		fileName := filepath.Base(filepath.FromSlash(relative))
		files = append(files, map[string]any{"name": fileName, "mimeType": browserFileMimeType(fileName, data), "data": base64.StdEncoding.EncodeToString(data)})
	}
	result, err := t.webControlSurfaceCall(ctx, "Surface.uploadFiles", surfaceID, map[string]any{"observationId": observationID, targetField: target, "files": files}, execCtx)
	if result.ExitCode != 0 {
		return browserFileTransportFailure(result), err
	}
	if result.Structured != nil {
		clean, _ := omitBrowserFileData(result.Structured).(map[string]any)
		result = webControlRestructured(result, clean)
	}
	return result, err
}

func omitBrowserFileData(raw any) any {
	switch value := raw.(type) {
	case map[string]any:
		clean := make(map[string]any, len(value))
		for field, item := range value {
			if field != "data" && field != "chunk" && field != "filePath" && field != "path" {
				clean[field] = omitBrowserFileData(item)
			}
		}
		return clean
	case []any:
		clean := make([]any, len(value))
		for index, item := range value {
			clean[index] = omitBrowserFileData(item)
		}
		return clean
	default:
		return value
	}
}

func browserFileTransportFailure(result ToolExecutionResult) ToolExecutionResult {
	// OS/Playwright messages may include a temporary host path. Keep machine
	// error classifications for recovery without forwarding those messages.
	code := result.Error
	if code == "" {
		code = "surface_file_transfer_failed"
	}
	clean := browserFileFailure(code, "browser file transfer did not complete; inspect current page state before retrying")
	details, _ := result.Structured["details"].(map[string]any)
	response, _ := result.Structured["response"].(map[string]any)
	hostError, _ := response["error"].(map[string]any)
	safeCode := func(raw any) string {
		value, valid := raw.(string)
		if !valid || len(value) > 100 {
			return ""
		}
		for _, char := range value {
			if char != '_' && char != '-' && (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
				return ""
			}
		}
		return value
	}
	if clientCode := safeCode(details["clientErrorType"]); clientCode != "" {
		clean.Structured["details"] = map[string]any{"clientErrorType": clientCode}
	}
	if state := safeCode(details["executionState"]); state != "" {
		if clean.Structured["details"] == nil {
			clean.Structured["details"] = map[string]any{}
		}
		clean.Structured["details"].(map[string]any)["executionState"] = state
	}
	safeResponse := map[string]any{"ok": false}
	if hostCode := safeCode(hostError["code"]); hostCode != "" {
		safeResponse["error"] = map[string]any{"code": hostCode}
	}
	// A failed upload can still have changed the page or opened a dialog. Keep
	// its action outcome and current observation for reconciliation; omitting
	// file bytes must not erase the evidence needed to avoid a duplicate action.
	if node, valid := response["result"].(map[string]any); valid {
		safeResult := map[string]any{}
		for _, field := range []string{"outcome", "error"} {
			if value := safeCode(node[field]); value != "" {
				safeResult[field] = value
			}
		}
		if observation, valid := node["observation"].(map[string]any); valid {
			safeResult["observation"] = omitBrowserFileData(observation)
		}
		if observationError, valid := node["observationError"].(map[string]any); valid {
			if code := safeCode(observationError["code"]); code != "" {
				safeResult["observationError"] = map[string]any{"code": code, "message": "read current page state before retrying the original action"}
			}
		}
		if len(safeResult) != 0 {
			safeResponse["result"] = safeResult
		}
	}
	if len(safeResponse) > 1 {
		clean.Structured["response"] = safeResponse
	}
	clean = webControlRestructured(clean, clean.Structured)
	return clean
}

func browserFileMimeType(name string, data []byte) string {
	if contentType := mime.TypeByExtension(filepath.Ext(name)); contentType != "" {
		return contentType
	}
	return http.DetectContentType(data)
}

func (t *RuntimeToolExecutor) webControlSurfaceDownloads(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	if failure, failed := webControlFields(args, "surfaceId", "action", "downloadId"); failed {
		return failure, nil
	}
	surfaceID, failure, failed := webControlSurfaceID(args, true)
	if failed {
		return failure, nil
	}
	action, failure, failed := webControlString(args, "action", true)
	if failed {
		return failure, nil
	}
	if action == "list" {
		if _, present := args["downloadId"]; present {
			return webControlInvalidArgs("downloadId is only valid with action save", "downloadId"), nil
		}
		result, err := t.webControlSurfaceCall(ctx, "Surface.getDownloads", surfaceID, nil, execCtx)
		if err != nil || result.ExitCode != 0 {
			return browserFileTransportFailure(result), err
		}
		return browserDownloadListResult(result), nil
	}
	if action != "save" {
		return webControlInvalidArgs("action must be list or save", "action"), nil
	}
	downloadID, failure, failed := webControlString(args, "downloadId", true)
	if failed {
		return failure, nil
	}
	if _, valid := t.browserFilesChatDir(execCtx); !valid {
		return browserFileFailure("surface_download_chat_unavailable", "the current Chat file context is unavailable"), nil
	}
	result, err := t.webControlSurfaceCall(ctx, "Surface.readDownload", surfaceID, map[string]any{"downloadId": downloadID}, execCtx)
	if err != nil || result.ExitCode != 0 {
		return browserFileTransportFailure(result), err
	}
	response, _ := result.Structured["response"].(map[string]any)
	node, _ := response["result"].(map[string]any)
	data, valid := node["data"].(string)
	fileName, nameOK := node["fileName"].(string)
	contentType, mimeOK := node["mimeType"].(string)
	sum, sumOK := node["sha256"].(string)
	size, sizeOK := webControlNumber(node["sizeBytes"])
	if !valid || !nameOK || !mimeOK || !sumOK || !sizeOK || node["downloadId"] != downloadID || size < 0 || size > browserFileMaxBytes || len(data) > base64.StdEncoding.EncodedLen(browserFileMaxBytes) {
		return browserFileFailure("surface_download_invalid_response", "the browser download response is invalid or exceeds 4 MiB"), nil
	}
	decoded, decodeErr := base64.StdEncoding.Strict().DecodeString(data)
	digest := sha256.Sum256(decoded)
	if decodeErr != nil || len(decoded) > browserFileMaxBytes || float64(len(decoded)) != size || sum != hex.EncodeToString(digest[:]) {
		return browserFileFailure("surface_download_integrity_failed", "the browser download size or SHA-256 did not match"), nil
	}
	metadata, saveErr := t.persistBrowserDownload(ctx, surfaceID, downloadID, fileName, contentType, decoded, execCtx)
	if saveErr != nil {
		code := "surface_download_save_failed"
		if errors.Is(saveErr, errBrowserDownloadConflict) {
			code = "surface_download_conflict"
		}
		return browserFileFailure(code, "the download could not be saved safely; existing Chat files were preserved"), nil
	}
	// Never return the original byte-bearing response, including on validation
	// or persistence failure. No model result, history or tool spill sees data.
	return structuredResult(map[string]any{"transport": "reverse-websocket", "response": map[string]any{"ok": true, "method": "Surface.readDownload", "result": metadata}}), nil
}

func browserDownloadListResult(result ToolExecutionResult) ToolExecutionResult {
	response, _ := result.Structured["response"].(map[string]any)
	node, _ := response["result"].(map[string]any)
	items, valid := node["downloads"].([]any)
	if !valid || len(items) > 9 {
		return browserFileFailure("surface_download_invalid_response", "the browser download list is invalid")
	}
	downloads := make([]any, 0, len(items))
	for index, raw := range items {
		item, valid := raw.(map[string]any)
		if !valid {
			return browserFileFailure("surface_download_invalid_response", "the browser download list is invalid")
		}
		// Eight actual transfers may be followed by one explicit refusal notice.
		// Keeping the notice must never hide the eight existing downloadable files.
		if index >= 8 && (item["status"] != "failed" || item["error"] != "file_transfer_limit") {
			return browserFileFailure("surface_download_invalid_response", "the browser download list exceeds its transfer limit")
		}
		entry := map[string]any{}
		for _, field := range []string{"downloadId", "fileName", "mimeType", "status"} {
			if value, present := item[field]; present {
				text, valid := value.(string)
				if !valid || len(text) > 4096 {
					return browserFileFailure("surface_download_invalid_response", "the browser download metadata is invalid")
				}
				if field == "fileName" {
					text = browserDownloadFileName(text)
				}
				entry[field] = text
			}
		}
		if id, _ := entry["downloadId"].(string); id == "" {
			return browserFileFailure("surface_download_invalid_response", "the browser download identity is missing")
		}
		if errorCode, present := item["error"]; present {
			if errorCode != "file_transfer_limit" && errorCode != "download_failed" {
				return browserFileFailure("surface_download_invalid_response", "the browser download error metadata is invalid")
			}
			entry["error"] = errorCode
		}
		if raw, present := item["sizeBytes"]; present {
			if size, valid := webControlNumber(raw); valid && size >= 0 && size <= browserFileMaxBytes {
				entry["sizeBytes"] = size
			}
		}
		downloads = append(downloads, entry)
	}
	return structuredResult(map[string]any{"transport": "reverse-websocket", "response": map[string]any{"ok": true, "method": "Surface.getDownloads", "result": map[string]any{"downloads": downloads}}})
}

var errBrowserDownloadConflict = errors.New("download publication conflict")

type browserDownloadReceipt struct {
	ReferenceName string `json:"referenceName"`
	FileName      string `json:"fileName"`
	MimeType      string `json:"mimeType"`
	SizeBytes     int    `json:"sizeBytes"`
	SHA256        string `json:"sha256"`
}

func (t *RuntimeToolExecutor) persistBrowserDownload(ctx context.Context, surfaceID, downloadID, fileName, contentType string, data []byte, execCtx *ExecutionContext) (map[string]any, error) {
	browserDownloadPublication.Lock()
	defer browserDownloadPublication.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	chatDir, valid := t.browserFilesChatDir(execCtx)
	if !valid {
		return nil, errors.New("Chat unavailable")
	}
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(chatDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	fileName = browserDownloadFileName(fileName)
	if parsed, _, err := mime.ParseMediaType(contentType); err != nil {
		contentType = "application/octet-stream"
	} else {
		contentType = parsed
	}
	digest := sha256.Sum256(data)
	key := sha256.Sum256([]byte(surfaceID + "\x00" + downloadID))
	keyString := hex.EncodeToString(key[:])
	receiptPath := filepath.Join(chat.ToolRootDirName, "browser-downloads", keyString+".json")
	receipt := browserDownloadReceipt{ReferenceName: "chrome-download-" + keyString[:24] + "-" + fileName, FileName: fileName, MimeType: contentType, SizeBytes: len(data), SHA256: hex.EncodeToString(digest[:])}
	if existing, err := root.ReadFile(receiptPath); err == nil {
		var stored browserDownloadReceipt
		if len(existing) > 16<<10 || json.Unmarshal(existing, &stored) != nil || stored != receipt {
			return nil, errBrowserDownloadConflict
		}
		if err := browserVerifyPublishedFile(root, stored.ReferenceName, data); err != nil {
			return nil, err
		}
		return browserDownloadMetadata(downloadID, stored), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	session := t.policySession(execCtx)
	session.ChatRoot = chatDir
	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, session, filetools.WriteAccess, filepath.Join(chatDir, receipt.ReferenceName))
	if err != nil || access.Blocked || !access.AllowedByWhitelist && !access.AutoApproved || !filetools.PathInSessionChat(session, access.Path) {
		return nil, errors.New("Chat write blocked")
	}
	if err := root.MkdirAll(filepath.Dir(receiptPath), 0o700); err != nil {
		return nil, err
	}
	if err := browserPublishFile(root, receipt.ReferenceName, data); err != nil {
		return nil, err
	}
	receiptData, _ := json.Marshal(receipt)
	if err := browserPublishFile(root, receiptPath, receiptData); err != nil {
		return nil, err
	}
	return browserDownloadMetadata(downloadID, receipt), nil
}

func browserDownloadMetadata(downloadID string, receipt browserDownloadReceipt) map[string]any {
	reference, _ := chat.BuildChatAliasRef(receipt.ReferenceName)
	return map[string]any{"downloadId": downloadID, "saved": true, "dataOmitted": true, "referenceName": receipt.ReferenceName, "url": reference, "fileName": receipt.FileName, "mimeType": receipt.MimeType, "sizeBytes": receipt.SizeBytes, "sha256": receipt.SHA256}
}

func browserVerifyPublishedFile(root *os.Root, name string, data []byte) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(data)) {
		return errBrowserDownloadConflict
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, browserFileMaxBytes+1)); err != nil {
		return err
	}
	want := sha256.Sum256(data)
	if hex.EncodeToString(hash.Sum(nil)) != hex.EncodeToString(want[:]) {
		return errBrowserDownloadConflict
	}
	return nil
}

func browserPublishFile(root *os.Root, name string, data []byte) error {
	if _, err := root.Lstat(name); err == nil {
		return browserVerifyPublishedFile(root, name, data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := ".browser-download-" + rand.Text() + ".tmp"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	// Root.Link publishes a fully written file without replacing an existing
	// name on both macOS and Windows. A collision is verified, never overwritten.
	if err := root.Link(temporary, name); err != nil {
		if errors.Is(err, os.ErrExist) {
			return browserVerifyPublishedFile(root, name, data)
		}
		return err
	}
	return nil
}

func browserDownloadFileName(raw string) string {
	name := filepath.Base(strings.ReplaceAll(raw, `\`, "/"))
	var safe []rune
	bytes := 0
	for _, char := range name {
		if unicode.IsControl(char) || strings.ContainsRune(`<>:"/\|?*`, char) {
			char = '_'
		}
		width := utf8.RuneLen(char)
		if bytes+width > 120 {
			break
		}
		safe = append(safe, char)
		bytes += width
	}
	name = strings.Trim(string(safe), ". ")
	if name == "" {
		return "download.bin"
	}
	// File names are also returned as display/download metadata, so normalize
	// Windows reserved names even on a macOS server, independent of the prefix
	// applied to the actual Chat file reference.
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	reserved := stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || stem == "CLOCK$"
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		reserved = true
	}
	if reserved {
		name = "_" + name
	}
	return name
}
