package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/credentialview"
	"agent-platform/internal/filetools"
	"agent-platform/internal/multimodal"
	"agent-platform/internal/runtimeenv"
	"agent-platform/internal/textcodec"
)

func (t *RuntimeToolExecutor) invokeRead(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	accessSession := t.policySession(execCtx)
	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, accessSession, filetools.ReadAccess, stringArg(args, "filePath"))
	if err != nil {
		return filePathResolutionError("file_read_invalid_path", err), nil
	}
	if access.Blocked {
		return fileToolError("file_read_path_blocked", access.Reason), nil
	}
	if filetools.IsBlockedDeviceFile(access.Path) {
		return fileToolError("file_read_device_blocked", "device file is blocked"), nil
	}
	if !access.AllowedByWhitelist && !access.AutoApproved && !filetools.ConsumeReadApproval(execCtx, access) {
		return fileAccessApprovalRequired("file_read_approval_required", "read超出允许目录", access), nil
	}
	resolved := filetools.ResolvedPath{Raw: access.RawPath, Path: access.Path, Root: access.Root}
	info, err := os.Stat(resolved.Path)
	if err != nil {
		return fileToolError("file_read_failed", err.Error()), nil
	}
	if info.IsDir() {
		return fileToolError("file_read_is_directory", "path is a directory"), nil
	}

	offset := int64Arg(args, "offset")
	limit := int64Arg(args, "limit")
	lineNumbered := addLineNumbersArg(args)
	requestedEncoding := stringArg(args, "encoding")
	window := ReadWindow{Offset: max(offset, 0), Limit: max(limit, 0), LineNumbered: lineNumbered}
	if execCtx != nil && strings.TrimSpace(requestedEncoding) == "" {
		if snap, ok := execCtx.ReadFileState[resolved.Path]; ok &&
			snap.ModifiedUnixMs == info.ModTime().UnixMilli() &&
			snap.SizeBytes == info.Size() &&
			snap.Source == "read" &&
			snap.LastRead == window &&
			!fileSnapshotExpired(snap, time.Now().UnixMilli(), t.fileReadMaxAge()) &&
			snap.SHA256 != "" && fileSHA256(resolved.Path) == snap.SHA256 {
			return structuredResult(map[string]any{
				"filePath": resolved.Path,
				"kind":     "unchanged",
				// The unchanged marker has no new file observation time. Omit the
				// optional modifiedUnixMs field instead of leaking a sentinel zero
				// into a public tool.result stream payload.
				"sizeBytes": int64(0),
				"message":   "File unchanged since last read. Refer to the earlier read result in this conversation.",
			}), nil
		}
	}
	if image, handled, result := readImageFile(resolved.Path, info); handled && credentialview.FromConfig(t.cfg).Source(resolved.Path) == credentialview.Ordinary {
		if result.ExitCode != 0 || result.Error != "" {
			return result, nil
		}
		t.recordReadProof(execCtx, resolved.Path, ReadFileSnapshot{
			ModifiedUnixMs: info.ModTime().UnixMilli(),
			SizeBytes:      info.Size(),
			SHA256:         image["sha256"].(string),
			LastRead:       window,
			ReadAtUnixMs:   time.Now().UnixMilli(),
			Source:         "read",
		})
		appendAccessPolicyMetadata(image, access)
		return structuredResult(image), nil
	}
	if filetools.IsBinaryExtension(resolved.Path) {
		return fileToolError("file_read_binary_unsupported", "binary file extension is not supported by read"), nil
	}
	file, err := os.Open(resolved.Path)
	if err != nil {
		return fileToolError("file_read_failed", err.Error()), nil
	}
	defer file.Close()

	startLine := 1
	if offset > 0 {
		startLine = int(offset)
	}
	scan, err := scanFileWindow(file, startLine, int(window.Limit), maxInt(t.cfg.FileTools.MaxReadBytes, 1<<20))
	if err != nil {
		return fileToolError("file_read_failed", err.Error()), nil
	}
	payload := map[string]any{
		"filePath":       resolved.Path,
		"sizeBytes":      info.Size(),
		"sha256":         scan.SHA256,
		"mode":           info.Mode().String(),
		"modifiedUnixMs": info.ModTime().UnixMilli(),
		"totalLines":     scan.TotalLines,
		"startLine":      startLine,
	}
	wholeFile := startLine == 1 && window.Limit == 0 && !scan.WindowCut
	var covered []LineRange
	decoded, ok, decodeErr := decodeFileWindow(scan, requestedEncoding, t.runtimeInfo())
	policy := credentialview.FromConfig(t.cfg)
	switch {
	case decodeErr != nil:
		return fileToolError("file_read_invalid_encoding", decodeErr.Error()), nil
	case !ok:
		payload["encoding"] = "base64"
		payload["kind"] = "binary"
		payload["contentBase64"] = base64.StdEncoding.EncodeToString(scan.Window)
		if policy.Source(resolved.Path) != credentialview.Ordinary {
			payload["contentBase64"] = credentialview.Hidden
			payload["redacted"] = true
		}
		payload["hasMore"] = !wholeFile
		if wholeFile && scan.TotalLines > 0 {
			covered = []LineRange{{Start: 1, End: scan.TotalLines}}
		}
	case policy.Source(resolved.Path) != credentialview.Ordinary:
		// Credential views mask the whole document, so they are not paged.
		content := policy.Text(resolved.Path, decoded.Content, !wholeFile)
		payload["encoding"] = decoded.Encoding
		payload["kind"] = "text"
		payload["content"] = content
		payload["hasMore"] = false
		if content != decoded.Content {
			payload["redacted"] = true
		}
		if wholeFile {
			payload["endLine"] = scan.TotalLines
			if scan.TotalLines > 0 {
				covered = []LineRange{{Start: 1, End: scan.TotalLines}}
			}
		} else {
			payload["endLine"] = startLine - 1
		}
	default:
		lastLineComplete := !scan.WindowCut || bytes.HasSuffix(scan.Window, []byte("\n"))
		page := fitFilePage(decoded.Content, startLine, scan.TotalLines, lineNumbered, lastLineComplete, ToolModelOutputMaxBytes)
		endLine := startLine + page.Lines - 1
		nextOffset := endLine + 1
		if page.LongLineTruncated {
			nextOffset = startLine + 1
			payload["longLineTruncated"] = startLine
		}
		if page.Lines > 0 {
			covered = []LineRange{{Start: startLine, End: endLine}}
		}
		payload["encoding"] = decoded.Encoding
		payload["kind"] = "text"
		payload["content"] = page.Content
		payload["endLine"] = endLine
		payload["hasMore"] = nextOffset <= scan.TotalLines
		if nextOffset <= scan.TotalLines {
			payload["nextOffset"] = nextOffset
		}
		if startLine > max(scan.TotalLines, 1) {
			payload["message"] = fmt.Sprintf("offset %d is beyond the end of the file (totalLines=%d)", startLine, scan.TotalLines)
		}
		if lineNumbered {
			payload["lineNumbered"] = true
		}
	}
	t.recordReadProof(execCtx, resolved.Path, ReadFileSnapshot{
		ModifiedUnixMs: info.ModTime().UnixMilli(),
		SizeBytes:      info.Size(),
		SHA256:         scan.SHA256,
		TotalLines:     scan.TotalLines,
		Ranges:         covered,
		LastRead:       window,
		ReadAtUnixMs:   time.Now().UnixMilli(),
		Source:         "read",
	})
	appendAccessPolicyMetadata(payload, access)
	return structuredResult(payload), nil
}

// decodeFileWindow decodes the captured window. A whole-file UTF-8 result
// fixes the encoding for every page; otherwise the window is detected alone.
func decodeFileWindow(scan fileWindowScan, requestedEncoding string, env runtimeenv.Info) (textcodec.DecodedText, bool, error) {
	if strings.TrimSpace(requestedEncoding) == "" && scan.UTF8 {
		window := scan.Window
		// A window cut by the raw byte limit may end inside a rune.
		for scan.WindowCut && len(window) > 0 && !utf8.Valid(window) {
			window = window[:len(window)-1]
		}
		return textcodec.DecodedText{Content: string(window), Encoding: "utf-8"}, true, nil
	}
	return textcodec.DecodeFileText(scan.Window, requestedEncoding, env)
}

func (t *RuntimeToolExecutor) invokeWrite(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	accessCfg := t.sessionFileToolsConfig(filetools.WriteAccess, execCtx)
	accessSession := t.policySession(execCtx)
	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, accessSession, filetools.WriteAccess, stringArg(args, "filePath"))
	if err != nil {
		return filePathResolutionError("file_write_invalid_plan", err), nil
	}
	if access.Blocked {
		return fileToolError("file_write_path_blocked", access.Reason), nil
	}
	if !access.AllowedByWhitelist && !access.AutoApproved && !filetools.ConsumeAccessApproval(execCtx, access) {
		return fileAccessApprovalRequired("file_write_path_approval_required", "write超出允许目录", access), nil
	}
	plan, err := filetools.BuildWritePlanWithAccess(access, accessCfg, args)
	if err != nil {
		return fileToolError("file_write_invalid_plan", err.Error()), nil
	}
	requiresWriteApproval := t.cfg.FileTools.RequireWriteApproval && !writeAllowedBySessionHostAccess(execCtx, plan.FilePath) && !writeAllowedBySessionWorkspace(execCtx, plan.FilePath) && !writeAllowedBySessionTemp(execCtx, plan.FilePath) && !writeAutoApprovedByAccessLevel(access)
	if requiresWriteApproval && !filetools.ConsumeWriteApproval(execCtx, plan) {
		result := structuredResultWithExit(map[string]any{
			"error":       "file_write_approval_required",
			"message":     "file write requires user approval",
			"filePath":    plan.FilePath,
			"command":     plan.CommandText,
			"fingerprint": plan.Fingerprint,
			"ruleKey":     plan.RuleKey,
		}, -1)
		result.Error = "file_write_approval_required"
		return result, nil
	}
	if t.cfg.FileTools.RequireReadBeforeWrite {
		if result, ok := t.validateReadBeforeWrite(plan.FilePath, execCtx); ok {
			return result, nil
		}
	}
	before, beforeExists := fileSHA256IfExists(plan.FilePath)
	beforeRaw := []byte(nil)
	beforeContent := ""
	beforeEncoding := "utf-8"
	if beforeExists {
		data, err := os.ReadFile(plan.FilePath)
		if err != nil {
			return fileToolError("file_write_failed", err.Error()), nil
		}
		beforeRaw = data
		if decoded, ok, _ := textcodec.DecodeFileText(data, "", t.runtimeInfo()); ok {
			beforeContent = decoded.Content
			beforeEncoding = decoded.Encoding
		} else {
			beforeContent = string(data)
		}
	}
	writeEncoding := strings.TrimSpace(plan.Encoding)
	if writeEncoding == "" {
		writeEncoding = "utf-8"
		if beforeExists && beforeEncoding != "utf-8" {
			writeEncoding = beforeEncoding
		}
	}
	contentText := string(plan.Content)
	writeBytes, writeEncoding, err := textcodec.EncodeFileText(contentText, writeEncoding)
	if err != nil {
		return fileToolError("file_write_invalid_encoding", err.Error()), nil
	}
	if len(writeBytes) > maxInt(t.cfg.FileTools.MaxWriteBytes, 1<<20) {
		return fileToolError("file_write_invalid_plan", "encoded content exceeds max write bytes"), nil
	}
	if err := os.MkdirAll(filepath.Dir(plan.FilePath), 0o755); err != nil {
		return fileToolError("file_write_failed", err.Error()), nil
	}
	if err := atomicWriteFile(plan.FilePath, writeBytes); err != nil {
		return fileToolError("file_write_failed", err.Error()), nil
	}
	if execCtx != nil {
		execCtx.EnsureAuthoredScripts()
		execCtx.AuthoredScripts.Record(execCtx.ScriptOwner(), plan.FilePath, writeBytes, "", true)
	}
	after := bytesSHA256(writeBytes)
	info, _ := os.Stat(plan.FilePath)
	lineStats := computeLineDiffStats(beforeContent, contentText)
	_ = t.recordFileHistory(execCtx, plan.FilePath, beforeRaw, beforeExists, writeBytes, true)
	payload := map[string]any{
		"status":       "written",
		"filePath":     plan.FilePath,
		"bytesWritten": len(writeBytes),
		"encoding":     writeEncoding,
		"created":      !beforeExists,
		"overwritten":  beforeExists,
		"sha256":       after,
		"lineStats":    lineStatsPayload(lineStats),
	}
	if beforeExists {
		payload["previousSha256"] = before
	}
	if info != nil {
		payload["sizeBytes"] = info.Size()
		payload["modifiedUnixMs"] = info.ModTime().UnixMilli()
		t.recordReadProof(execCtx, plan.FilePath, mutationReadProof(info, after, "write", CountTextLines(contentText), fullLineRange(CountTextLines(contentText))))
	}
	appendAccessPolicyMetadata(payload, access)
	t.appendFileChangeHookResults(ctx, execCtx, payload, FileChangeEvent{
		AgentKey:              fileChangeAgentKey(execCtx),
		ChatID:                fileChangeChatID(execCtx),
		RunID:                 fileChangeRunID(execCtx),
		WorkspaceRoot:         fileChangeWorkspaceRoot(execCtx),
		FilePath:              plan.FilePath,
		Operation:             "write",
		PreviousContentSHA256: before,
		ContentSHA256:         after,
		Content:               append([]byte(nil), writeBytes...),
		LineStats:             lineStats,
	})
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) invokeEdit(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	accessCfg := t.sessionFileToolsConfig(filetools.WriteAccess, execCtx)
	accessSession := t.policySession(execCtx)
	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, accessSession, filetools.WriteAccess, stringArg(args, "filePath"))
	if err != nil {
		return filePathResolutionError("file_edit_invalid_plan", err), nil
	}
	access.CommandText = "file_edit " + access.Path
	if access.Blocked {
		return fileToolError("file_edit_path_blocked", access.Reason), nil
	}
	if !access.AllowedByWhitelist && !access.AutoApproved && !filetools.ConsumeAccessApproval(execCtx, access) {
		return fileAccessApprovalRequired("file_edit_path_approval_required", "edit超出允许目录", access), nil
	}
	plan, err := filetools.BuildEditPlanWithAccess(access, accessCfg, args)
	if err != nil {
		return fileToolError("file_edit_invalid_plan", err.Error()), nil
	}
	requiresWriteApproval := t.cfg.FileTools.RequireWriteApproval && !writeAllowedBySessionHostAccess(execCtx, plan.FilePath) && !writeAllowedBySessionWorkspace(execCtx, plan.FilePath) && !writeAllowedBySessionTemp(execCtx, plan.FilePath) && !writeAutoApprovedByAccessLevel(access)
	if requiresWriteApproval && !filetools.ConsumeWriteApproval(execCtx, plan) {
		result := structuredResultWithExit(map[string]any{
			"error":       "file_edit_approval_required",
			"message":     "file edit requires user approval",
			"filePath":    plan.FilePath,
			"command":     plan.CommandText,
			"fingerprint": plan.Fingerprint,
			"ruleKey":     plan.RuleKey,
		}, -1)
		result.Error = "file_edit_approval_required"
		return result, nil
	}
	var proof ReadFileSnapshot
	proofRequired := false
	if t.cfg.FileTools.RequireReadBeforeWrite {
		snap, exists, result, blocked := t.readProofForMutation(plan.FilePath, execCtx, "file_edit")
		if blocked {
			return result, nil
		}
		proof, proofRequired = snap, exists
	}

	before, beforeExists := fileSHA256IfExists(plan.FilePath)
	currentRaw := []byte(nil)
	currentContent := ""
	currentEncoding := strings.TrimSpace(plan.Encoding)
	created := !beforeExists
	if beforeExists {
		info, err := os.Stat(plan.FilePath)
		if err != nil {
			return fileToolError("file_edit_failed", err.Error()), nil
		}
		if info.IsDir() {
			return fileToolError("file_edit_is_directory", "path is a directory"), nil
		}
		if filetools.IsBinaryExtension(plan.FilePath) {
			return fileToolError("file_edit_binary_unsupported", "binary file extension is not supported by edit"), nil
		}
		data, err := os.ReadFile(plan.FilePath)
		if err != nil {
			return fileToolError("file_edit_failed", err.Error()), nil
		}
		currentRaw = data
		before = bytesSHA256(data)
		if proofRequired && before != proof.SHA256 {
			return fileToolError("file_edit_modified_since_read", "file has been modified since last read; re-read before writing"), nil
		}
		decoded, ok, decodeErr := textcodec.DecodeFileText(data, plan.Encoding, t.runtimeInfo())
		if decodeErr != nil {
			return fileToolError("file_edit_invalid_encoding", decodeErr.Error()), nil
		}
		if !ok {
			return fileToolError("file_edit_non_utf8_unsupported", "file content is not valid UTF-8 or a supported text encoding"), nil
		}
		currentContent = decoded.Content
		currentEncoding = decoded.Encoding
	} else if plan.OldString != "" {
		return fileToolError("file_edit_file_not_found", "file does not exist and oldString is not empty"), nil
	}
	if strings.TrimSpace(currentEncoding) == "" {
		currentEncoding = "utf-8"
	}

	normalizedContent, lineEndings := normalizeEditLineEndings(currentContent)
	oldString := normalizeEditString(plan.OldString)
	newString := normalizeEditString(plan.NewString)

	updatedContent := ""
	replacements := 0
	var lineEdits []LineEdit
	if oldString == "" {
		if beforeExists && normalizedContent != "" {
			return fileToolError("file_edit_file_exists", "oldString is empty but file already has content"), nil
		}
		updatedContent = newString
		replacements = 1
	} else {
		replacements = strings.Count(normalizedContent, oldString)
		if replacements == 0 {
			return fileEditStringNotFoundResult(normalizedContent, oldString, newString), nil
		}
		if replacements > 1 && !plan.ReplaceAll {
			return fileToolError("file_edit_multiple_matches", fmt.Sprintf("oldString matched %d times; set replaceAll=true or provide more context", replacements)), nil
		}
		lineEdits = lineEditsForMatches(normalizedContent, oldString, newString, plan.ReplaceAll)
		if proofRequired {
			if result, blocked := unseenEditRegionResult(proof, lineEdits); blocked {
				return result, nil
			}
		}
		if lineEndings == "MIXED" {
			updatedContent, replacements = replaceNormalizedMatchesPreservingLineEndings(currentContent, normalizedContent, oldString, newString, plan.ReplaceAll)
		} else if plan.ReplaceAll {
			updatedContent = strings.ReplaceAll(normalizedContent, oldString, newString)
		} else {
			updatedContent = strings.Replace(normalizedContent, oldString, newString, 1)
			replacements = 1
		}
	}

	if lineEndings == "CRLF" {
		updatedContent = strings.ReplaceAll(updatedContent, "\n", "\r\n")
	}
	updatedBytes, currentEncoding, err := textcodec.EncodeFileText(updatedContent, currentEncoding)
	if err != nil {
		return fileToolError("file_edit_invalid_encoding", err.Error()), nil
	}
	if len(updatedBytes) > maxInt(t.cfg.FileTools.MaxWriteBytes, 1<<20) {
		return fileToolError("file_edit_content_too_large", "edited content exceeds max write bytes"), nil
	}
	if err := os.MkdirAll(filepath.Dir(plan.FilePath), 0o755); err != nil {
		return fileToolError("file_edit_failed", err.Error()), nil
	}
	if err := atomicWriteFile(plan.FilePath, updatedBytes); err != nil {
		return fileToolError("file_edit_failed", err.Error()), nil
	}
	if execCtx != nil {
		execCtx.EnsureAuthoredScripts()
		execCtx.AuthoredScripts.Record(execCtx.ScriptOwner(), plan.FilePath, updatedBytes, before, false)
	}
	after := bytesSHA256(updatedBytes)
	info, _ := os.Stat(plan.FilePath)
	lineStats := computeLineDiffStats(currentContent, updatedContent)
	_ = t.recordFileHistory(execCtx, plan.FilePath, currentRaw, beforeExists, updatedBytes, true)
	payload := map[string]any{
		"status":       "edited",
		"filePath":     plan.FilePath,
		"replacements": replacements,
		"replaceAll":   plan.ReplaceAll,
		"created":      created,
		"encoding":     currentEncoding,
		"sha256":       after,
		"lineStats":    lineStatsPayload(lineStats),
	}
	if beforeExists {
		payload["previousSha256"] = before
	}
	if info != nil {
		payload["sizeBytes"] = info.Size()
		payload["modifiedUnixMs"] = info.ModTime().UnixMilli()
		totalLines := CountTextLines(updatedContent)
		covered := fullLineRange(totalLines)
		if proofRequired && proof.TotalLines > 0 {
			// Lines the model has not read stay unread after the edit.
			covered = ClampLineRanges(ApplyLineEdits(proof.Ranges, lineEdits), totalLines)
		}
		t.recordReadProof(execCtx, plan.FilePath, mutationReadProof(info, after, "edit", totalLines, covered))
	}
	appendAccessPolicyMetadata(payload, access)
	t.appendFileChangeHookResults(ctx, execCtx, payload, FileChangeEvent{
		AgentKey:              fileChangeAgentKey(execCtx),
		ChatID:                fileChangeChatID(execCtx),
		RunID:                 fileChangeRunID(execCtx),
		WorkspaceRoot:         fileChangeWorkspaceRoot(execCtx),
		FilePath:              plan.FilePath,
		Operation:             "edit",
		PreviousContentSHA256: before,
		ContentSHA256:         after,
		Content:               append([]byte(nil), updatedBytes...),
		LineStats:             lineStats,
	})
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) appendFileChangeHookResults(ctx context.Context, execCtx *ExecutionContext, payload map[string]any, event FileChangeEvent) {
	if t == nil || len(t.fileChangeHooks) == 0 || execCtx == nil {
		return
	}
	if strings.TrimSpace(event.WorkspaceRoot) == "" {
		return
	}
	if !filetools.PathInSessionWorkspace(execCtx.Session, event.FilePath) {
		return
	}
	results := make([]FileChangeHookResult, 0, len(t.fileChangeHooks))
	for _, hook := range t.fileChangeHooks {
		if hook == nil {
			continue
		}
		result := hook.AfterFileChange(ctx, event)
		if fileChangeHookResultEmpty(result) {
			continue
		}
		results = append(results, result)
	}
	if len(results) > 0 {
		payload["hooks"] = results
	}
}

func fileChangeHookResultEmpty(result FileChangeHookResult) bool {
	return strings.TrimSpace(result.Name) == "" &&
		strings.TrimSpace(result.Status) == "" &&
		strings.TrimSpace(result.LanguageID) == "" &&
		strings.TrimSpace(result.FilePath) == "" &&
		len(result.Diagnostics) == 0 &&
		strings.TrimSpace(result.Reason) == "" &&
		strings.TrimSpace(result.Message) == "" &&
		len(result.Data) == 0
}

func fileChangeAgentKey(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	if value := strings.TrimSpace(execCtx.Session.AgentKey); value != "" {
		return value
	}
	return strings.TrimSpace(execCtx.Request.AgentKey)
}

func fileChangeChatID(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	return strings.TrimSpace(execCtx.Request.ChatID)
}

func fileChangeRunID(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	return strings.TrimSpace(execCtx.Request.RunID)
}

func fileChangeWorkspaceRoot(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	return filetools.SessionWorkspaceRoot(execCtx.Session)
}

func (t *RuntimeToolExecutor) sessionFileToolsConfig(_ filetools.AccessMode, _ *ExecutionContext) config.FileToolsConfig {
	return t.cfg.FileTools
}

func writeAllowedBySessionWorkspace(execCtx *ExecutionContext, path string) bool {
	if execCtx == nil {
		return false
	}
	return filetools.PathInSessionWorkspace(execCtx.Session, path) ||
		filetools.PathInSessionChat(execCtx.Session, path)
}

func writeAllowedBySessionHostAccess(execCtx *ExecutionContext, path string) bool {
	if execCtx == nil {
		return false
	}
	return filetools.PathInSessionHostWriteRoot(execCtx.Session, path)
}

func writeAllowedBySessionTemp(execCtx *ExecutionContext, path string) bool {
	if execCtx == nil {
		return false
	}
	return filetools.PathInSessionTemp(execCtx.Session, path)
}

func accessPolicySession(execCtx *ExecutionContext) QuerySession {
	if execCtx == nil {
		return QuerySession{}
	}
	if execCtx.RunControl != nil {
		if accessLevel, _ := execCtx.RunControl.AccessLevelSnapshot(); strings.TrimSpace(accessLevel) != "" {
			execCtx.AccessLevel = accessLevel
			execCtx.Session.AccessLevel = accessLevel
		}
	}
	session := execCtx.Session
	if strings.TrimSpace(session.AccessLevel) == "" {
		session.AccessLevel = execCtx.AccessLevel
	}
	return session
}

func appendAccessPolicyMetadata(payload map[string]any, access filetools.AccessPlan) {
	if !access.AutoApproved {
		return
	}
	payload["accessPolicy"] = map[string]any{
		"accessLevel": access.AccessLevel,
		"decision":    "auto_approved",
		"ruleKey":     access.RuleKey,
	}
}

func writeAutoApprovedByAccessLevel(access filetools.AccessPlan) bool {
	switch strings.ToLower(strings.TrimSpace(access.AccessLevel)) {
	case AccessLevelAutoApprove, AccessLevelFullAccess:
		return true
	default:
		return access.AutoApproved
	}
}

func readImageFile(path string, info os.FileInfo) (map[string]any, bool, ToolExecutionResult) {
	if !filetools.IsSupportedImageExtension(path) {
		return nil, false, ToolExecutionResult{}
	}
	image, err := multimodal.LoadImageFile(path, "", multimodal.ImageLoadOptions{
		MaxBytes:               filetools.MaxInlineImageBytes,
		ReencodeThresholdBytes: 0,
	})
	if errors.Is(err, multimodal.ErrUnsupportedImageMime) {
		return nil, false, ToolExecutionResult{}
	}
	if errors.Is(err, multimodal.ErrImageTooLarge) {
		return nil, true, fileToolError("file_read_image_too_large", fmt.Sprintf("image exceeds max inline bytes: %d", filetools.MaxInlineImageBytes))
	}
	if err != nil {
		return nil, true, fileToolError("file_read_failed", err.Error())
	}
	return map[string]any{
		"filePath":       path,
		"kind":           "image",
		"mimeType":       image.MimeType,
		"sizeBytes":      info.Size(),
		"modifiedUnixMs": info.ModTime().UnixMilli(),
		"sha256":         image.SHA256,
		"contentBase64":  image.DataBase64,
	}, true, ToolExecutionResult{}
}

// validateReadBeforeWrite requires every line of an existing file to have
// been read, because file_write replaces the whole document.
func (t *RuntimeToolExecutor) validateReadBeforeWrite(path string, execCtx *ExecutionContext) (ToolExecutionResult, bool) {
	proof, exists, result, blocked := t.readProofForMutation(path, execCtx, "file_write")
	if blocked {
		return result, true
	}
	if exists && !proof.Complete() {
		return fileToolErrorWithFields("file_write_partial_read", "file was not fully read; read the remaining lines with file_read (follow nextOffset until hasMore=false) before overwriting", map[string]any{
			"totalLines":   proof.TotalLines,
			"unseenRanges": lineRangesPayload(proof.Uncovered(1, proof.TotalLines)),
		}), true
	}
	return ToolExecutionResult{}, false
}

// readProofForMutation returns the current read proof of an existing file.
// It rejects a missing, expired or outdated proof; line coverage is checked
// by the caller because file_write and file_edit need different lines.
func (t *RuntimeToolExecutor) readProofForMutation(path string, execCtx *ExecutionContext, errorPrefix string) (ReadFileSnapshot, bool, ToolExecutionResult, bool) {
	modifiedCode := errorPrefix + "_modified_since_read"
	if errorPrefix == "file_write" {
		modifiedCode = "file_modified_since_read"
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ReadFileSnapshot{}, false, ToolExecutionResult{}, false
		}
		return ReadFileSnapshot{}, false, fileToolError(errorPrefix+"_failed", err.Error()), true
	}
	var snap ReadFileSnapshot
	var ok bool
	if execCtx != nil {
		snap, ok = execCtx.ReadFileState[path]
	}
	if !ok {
		snap, ok = t.loadChatFileVersionSnapshot(execCtx, path)
	}
	if !ok {
		return ReadFileSnapshot{}, true, fileToolError(errorPrefix+"_not_read", "file exists but has not been read in the configured scope; call file_read before retrying"), true
	}
	now := time.Now().UnixMilli()
	maxAge := t.fileReadMaxAge()
	if fileSnapshotExpired(snap, now, maxAge) {
		age := int64(0)
		message := "文件缓存过期：观察时间缺失或异常，请重新调用 file_read 读取文件。"
		if snap.ReadAtUnixMs > 0 && snap.ReadAtUnixMs <= now {
			age = now - snap.ReadAtUnixMs
			message = fmt.Sprintf("文件缓存过期：距上次有效观察已过 %.1f 分钟，有效期为 %.1f 分钟，请重新调用 file_read 读取文件。", float64(age)/60000, maxAge.Minutes())
		}
		return ReadFileSnapshot{}, true, fileToolErrorWithFields(errorPrefix+"_cache_expired", message, map[string]any{
			"observedAtUnixMs": snap.ReadAtUnixMs,
			"ageMs":            age,
			"maxAgeMs":         maxAge.Milliseconds(),
		}), true
	}
	currentSHA := fileSHA256(path)
	if currentSHA == "" || currentSHA != snap.SHA256 {
		return ReadFileSnapshot{}, true, fileToolError(modifiedCode, "file has been modified since last read; re-read before writing"), true
	}
	snap.ModifiedUnixMs = info.ModTime().UnixMilli()
	snap.SizeBytes = info.Size()
	if execCtx != nil {
		if execCtx.ReadFileState == nil {
			execCtx.ReadFileState = map[string]ReadFileSnapshot{}
		}
		execCtx.ReadFileState[path] = snap
	}
	return snap, true, ToolExecutionResult{}, false
}

// unseenEditRegionResult rejects an edit whose matched lines were not all
// delivered to the model for the current file version.
func unseenEditRegionResult(proof ReadFileSnapshot, edits []LineEdit) (ToolExecutionResult, bool) {
	var unseen []LineRange
	for _, edit := range edits {
		unseen = append(unseen, proof.Uncovered(edit.Start, edit.End)...)
	}
	unseen = NormalizeLineRanges(unseen)
	if len(unseen) == 0 {
		return ToolExecutionResult{}, false
	}
	matches := make([]LineRange, 0, len(edits))
	for _, edit := range edits {
		matches = append(matches, LineRange{Start: edit.Start, End: edit.End})
	}
	return fileToolErrorWithFields("file_edit_unseen_region", "oldString matches lines that were not read; read unseenRanges with file_read (offset/limit) and retry", map[string]any{
		"unseenRanges": lineRangesPayload(unseen),
		"matchRanges":  lineRangesPayload(matches),
	}), true
}

// lineEditsForMatches locates the non-overlapping matches that the edit
// replaces, in the same order strings.Replace/ReplaceAll apply them.
func lineEditsForMatches(content string, oldString string, newString string, replaceAll bool) []LineEdit {
	if oldString == "" {
		return nil
	}
	delta := strings.Count(newString, "\n") - strings.Count(oldString, "\n")
	span := strings.Count(oldString[:len(oldString)-1], "\n")
	var edits []LineEdit
	line := 1
	scanned := 0
	for from := 0; from <= len(content); {
		index := strings.Index(content[from:], oldString)
		if index < 0 {
			break
		}
		start := from + index
		line += strings.Count(content[scanned:start], "\n")
		scanned = start
		edits = append(edits, LineEdit{Start: line, End: line + span, Delta: delta})
		if !replaceAll {
			break
		}
		from = start + len(oldString)
	}
	return edits
}

func lineRangesPayload(ranges []LineRange) []any {
	out := make([]any, 0, len(ranges))
	for _, item := range ranges {
		out = append(out, map[string]any{"start": item.Start, "end": item.End})
	}
	return out
}

func fullLineRange(totalLines int) []LineRange {
	if totalLines <= 0 {
		return nil
	}
	return []LineRange{{Start: 1, End: totalLines}}
}

func (t *RuntimeToolExecutor) fileReadMaxAge() time.Duration {
	if t.cfg.FileTools.ReadBeforeWriteMaxAge > 0 {
		return t.cfg.FileTools.ReadBeforeWriteMaxAge
	}
	return time.Hour
}

func fileSnapshotExpired(snap ReadFileSnapshot, now int64, maxAge time.Duration) bool {
	return snap.ReadAtUnixMs <= 0 || snap.ReadAtUnixMs > now || now-snap.ReadAtUnixMs > maxAge.Milliseconds()
}

func normalizeEditLineEndings(content string) (string, string) {
	hasCRLF := strings.Contains(content, "\r\n")
	withoutCRLF := strings.ReplaceAll(content, "\r\n", "")
	hasBareLF := strings.Contains(withoutCRLF, "\n")
	if hasCRLF {
		normalized := strings.ReplaceAll(content, "\r\n", "\n")
		if hasBareLF {
			return normalized, "MIXED"
		}
		return normalized, "CRLF"
	}
	return content, "LF"
}

func normalizeEditString(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

func fileEditStringNotFoundResult(normalizedContent string, oldString string, newString string) ToolExecutionResult {
	newStringMatches := 0
	if newString != "" {
		newStringMatches = strings.Count(normalizedContent, newString)
	}
	candidate := removeOneLeadingTabPerLine(oldString)
	candidateMatches := 0
	if candidate != oldString && candidate != "" {
		candidateMatches = strings.Count(normalizedContent, candidate)
	}
	diagnostics := map[string]any{
		"newStringMatches":                           newStringMatches,
		"alreadyAppliedLikely":                       newStringMatches > 0,
		"lineNumberedIndentLikely":                   candidateMatches > 0,
		"candidateMatchesAfterRemovingOneLeadingTab": candidateMatches,
		"oldStringBytes":                             len([]byte(oldString)),
	}
	return fileToolErrorWithFields("file_edit_string_not_found", "oldString was not found in file. If you copied from file_read output with line numbers, the tab after the line number is not part of the file content; re-read with addLineNumbers=false and retry.", map[string]any{
		"diagnostics": diagnostics,
	})
}

func removeOneLeadingTabPerLine(content string) string {
	lines := strings.SplitAfter(content, "\n")
	for idx, line := range lines {
		if strings.HasPrefix(line, "\t") {
			lines[idx] = line[1:]
		}
	}
	return strings.Join(lines, "")
}

func replaceNormalizedMatchesPreservingLineEndings(original string, normalized string, oldString string, newString string, replaceAll bool) (string, int) {
	mapping := normalizedToOriginalByteOffsets(original)
	var builder strings.Builder
	lastOriginal := 0
	searchStart := 0
	replacements := 0
	for {
		idx := strings.Index(normalized[searchStart:], oldString)
		if idx < 0 {
			break
		}
		normalizedStart := searchStart + idx
		normalizedEnd := normalizedStart + len(oldString)
		originalStart := mapping[normalizedStart]
		originalEnd := mapping[normalizedEnd]
		builder.WriteString(original[lastOriginal:originalStart])
		builder.WriteString(convertNormalizedLineEndings(newString, localLineEndingForReplacement(original, originalStart, originalEnd)))
		lastOriginal = originalEnd
		replacements++
		if !replaceAll {
			break
		}
		searchStart = normalizedEnd
	}
	builder.WriteString(original[lastOriginal:])
	return builder.String(), replacements
}

func normalizedToOriginalByteOffsets(content string) []int {
	offsets := make([]int, 0, len(content)+1)
	for idx := 0; idx < len(content); {
		offsets = append(offsets, idx)
		if content[idx] == '\r' && idx+1 < len(content) && content[idx+1] == '\n' {
			idx += 2
			continue
		}
		idx++
	}
	offsets = append(offsets, len(content))
	return offsets
}

func convertNormalizedLineEndings(content string, lineEnding string) string {
	if lineEnding == "CRLF" {
		return strings.ReplaceAll(content, "\n", "\r\n")
	}
	return content
}

func localLineEndingForReplacement(content string, start int, end int) string {
	if lineEnding := dominantLineEnding(content[start:end]); lineEnding != "" {
		return lineEnding
	}
	if lineEnding := nextLineEnding(content, end); lineEnding != "" {
		return lineEnding
	}
	if lineEnding := previousLineEnding(content, start); lineEnding != "" {
		return lineEnding
	}
	if lineEnding := dominantLineEnding(content); lineEnding != "" {
		return lineEnding
	}
	return "LF"
}

func dominantLineEnding(content string) string {
	crlf, lf := countLineEndings(content)
	switch {
	case crlf > lf:
		return "CRLF"
	case lf > 0:
		return "LF"
	case crlf > 0:
		return "CRLF"
	default:
		return ""
	}
}

func countLineEndings(content string) (int, int) {
	crlf := 0
	lf := 0
	for idx := 0; idx < len(content); idx++ {
		if content[idx] != '\n' {
			continue
		}
		if idx > 0 && content[idx-1] == '\r' {
			crlf++
		} else {
			lf++
		}
	}
	return crlf, lf
}

func nextLineEnding(content string, start int) string {
	for idx := start; idx < len(content); idx++ {
		if content[idx] != '\n' {
			continue
		}
		if idx > 0 && content[idx-1] == '\r' {
			return "CRLF"
		}
		return "LF"
	}
	return ""
}

func previousLineEnding(content string, start int) string {
	for idx := start - 1; idx >= 0; idx-- {
		if content[idx] != '\n' {
			continue
		}
		if idx > 0 && content[idx-1] == '\r' {
			return "CRLF"
		}
		return "LF"
	}
	return ""
}

// recordReadProof stores snap as the file's read proof, accumulating line
// ranges with an unexpired proof of the same version from this run or the
// Chat ledger.
func (t *RuntimeToolExecutor) recordReadProof(execCtx *ExecutionContext, path string, snap ReadFileSnapshot) {
	if execCtx == nil {
		return
	}
	if execCtx.ReadFileState == nil {
		execCtx.ReadFileState = map[string]ReadFileSnapshot{}
	}
	prev, ok := execCtx.ReadFileState[path]
	if !ok {
		prev, ok = t.loadChatFileVersionSnapshot(execCtx, path)
	}
	if ok && !fileSnapshotExpired(prev, time.Now().UnixMilli(), t.fileReadMaxAge()) {
		snap = MergeReadFileSnapshot(prev, snap)
	}
	execCtx.ReadFileState[path] = snap
	t.recordChatFileVersion(execCtx, path, snap)
}

func mutationReadProof(info os.FileInfo, sha string, source string, totalLines int, covered []LineRange) ReadFileSnapshot {
	return ReadFileSnapshot{
		ModifiedUnixMs: info.ModTime().UnixMilli(),
		SizeBytes:      info.Size(),
		SHA256:         sha,
		TotalLines:     totalLines,
		Ranges:         covered,
		ReadAtUnixMs:   time.Now().UnixMilli(),
		Source:         source,
	}
}

func addLineNumbersArg(args map[string]any) bool {
	if _, ok := args["addLineNumbers"]; !ok {
		return false
	}
	return boolArg(args, "addLineNumbers")
}

func fileToolError(code string, message string) ToolExecutionResult {
	return fileToolErrorWithFields(code, message, nil)
}

func filePathResolutionError(defaultCode string, err error) ToolExecutionResult {
	if err != nil && strings.Contains(err.Error(), "workspace_unavailable") {
		return fileToolError("workspace_unavailable", err.Error())
	}
	if err == nil {
		return fileToolError(defaultCode, "")
	}
	return fileToolError(defaultCode, err.Error())
}

func fileToolErrorWithFields(code string, message string, fields map[string]any) ToolExecutionResult {
	payload := map[string]any{
		"error":   code,
		"message": strings.TrimSpace(message),
	}
	for key, value := range fields {
		payload[key] = value
	}
	result := structuredResultWithExit(payload, -1)
	result.Error = code
	return result
}

func fileAccessApprovalRequired(code string, message string, plan filetools.AccessPlan) ToolExecutionResult {
	result := structuredResultWithExit(map[string]any{
		"error":       code,
		"message":     strings.TrimSpace(message),
		"filePath":    plan.Path,
		"command":     plan.CommandText,
		"fingerprint": plan.Fingerprint,
		"ruleKey":     plan.RuleKey,
		"root":        plan.Root,
	}, -1)
	result.Error = code
	return result
}

func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func fileSHA256IfExists(path string) (string, bool) {
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return fileSHA256(path), true
}

func bytesSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fileSHA256(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}
