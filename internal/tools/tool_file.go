package tools

import (
	"bufio"
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

	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/multimodal"
	"agent-platform/internal/textcodec"
)

func (t *RuntimeToolExecutor) invokeRead(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, accessPolicySessionWithFallback(execCtx, t.cfg.FileTools.WorkingDirectory), filetools.ReadAccess, stringArg(args, "file_path"))
	if err != nil {
		return fileToolError("file_read_invalid_path", err.Error()), nil
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
	snapshotOffset := int64(0)
	if offset > 0 {
		snapshotOffset = offset
	}
	snapshotLimit := int64(0)
	if limit > 0 {
		snapshotLimit = limit
	}
	if execCtx != nil && strings.TrimSpace(requestedEncoding) == "" {
		if execCtx.ReadFileState == nil {
			execCtx.ReadFileState = map[string]ReadFileSnapshot{}
		}
		if snap, ok := execCtx.ReadFileState[resolved.Path]; ok &&
			snap.ModifiedUnixMs == info.ModTime().UnixMilli() &&
			snap.SizeBytes == info.Size() &&
			snap.Offset == snapshotOffset &&
			snap.Limit == snapshotLimit &&
			snap.Source == "read" &&
			!snap.Partial &&
			!snap.Truncated &&
			snap.LineNumbered == lineNumbered {
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
	if image, handled, result := readImageFile(resolved.Path, info); handled {
		if result.ExitCode != 0 || result.Error != "" {
			return result, nil
		}
		if execCtx != nil {
			snap := recordReadSnapshot(execCtx, resolved.Path, info, image["sha256"].(string), snapshotOffset, snapshotLimit, "read", lineNumbered, false, false)
			t.recordChatFileVersion(execCtx, resolved.Path, snap, "read", false)
		}
		appendAccessPolicyMetadata(image, access)
		return structuredResult(image), nil
	}
	if filetools.IsBinaryExtension(resolved.Path) {
		return fileToolError("file_read_binary_unsupported", "binary file extension is not supported by read"), nil
	}

	maxBytes := maxInt(t.cfg.FileTools.MaxReadBytes, 1<<20)
	file, err := os.Open(resolved.Path)
	if err != nil {
		return fileToolError("file_read_failed", err.Error()), nil
	}
	defer file.Close()

	startLine := 1
	if offset > 0 {
		startLine = int(offset)
	}
	lineLimited := offset > 0 || limit > 0
	partial := startLine > 1 || limit > 0
	var data []byte
	truncated := false
	if lineLimited {
		data, truncated, err = readLineRangeLimited(file, startLine, int(limit), maxBytes)
		if err != nil {
			return fileToolError("file_read_failed", err.Error()), nil
		}
	} else {
		data, err = io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
		if err != nil {
			return fileToolError("file_read_failed", err.Error()), nil
		}
		truncated = len(data) > maxBytes
		if truncated {
			data = data[:maxBytes]
		}
	}

	sha := fileSHA256(resolved.Path)
	payload := map[string]any{
		"filePath":       resolved.Path,
		"sizeBytes":      info.Size(),
		"readBytes":      len(data),
		"sha256":         sha,
		"mode":           info.Mode().String(),
		"modifiedUnixMs": info.ModTime().UnixMilli(),
		"truncated":      truncated,
	}
	if lineLimited {
		payload["offset"] = startLine
		if limit > 0 {
			payload["limit"] = limit
		}
	}
	if decoded, ok, decodeErr := textcodec.DecodeFileText(data, requestedEncoding, t.runtimeInfo()); decodeErr != nil {
		return fileToolError("file_read_invalid_encoding", decodeErr.Error()), nil
	} else if ok {
		content := decoded.Content
		payload["encoding"] = decoded.Encoding
		payload["kind"] = "text"
		if lineNumbered {
			content = addLineNumbers(content, startLine)
			payload["lineNumbered"] = true
		}
		payload["content"] = content
	} else {
		payload["encoding"] = "base64"
		payload["kind"] = "binary"
		payload["contentBase64"] = base64.StdEncoding.EncodeToString(data)
	}
	if execCtx != nil {
		snap := recordReadSnapshot(execCtx, resolved.Path, info, sha, snapshotOffset, snapshotLimit, "read", lineNumbered, partial, truncated)
		t.recordChatFileVersion(execCtx, resolved.Path, snap, "read", truncated)
	}
	appendAccessPolicyMetadata(payload, access)
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) invokeWrite(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	accessCfg := t.sessionFileToolsConfig(filetools.WriteAccess, execCtx)
	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, accessPolicySessionWithFallback(execCtx, accessCfg.WorkingDirectory), filetools.WriteAccess, stringArg(args, "file_path"))
	if err != nil {
		return fileToolError("file_write_invalid_plan", err.Error()), nil
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
	requiresWriteApproval := t.cfg.FileTools.RequireWriteApproval && !writeAllowedBySessionHostAccess(execCtx, plan.FilePath) && !writeAllowedBySessionWorkspace(execCtx, plan.FilePath) && !writeAutoApprovedByAccessLevel(access)
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
	after := fileSHA256(plan.FilePath)
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
		if execCtx != nil {
			snap := recordReadSnapshot(execCtx, plan.FilePath, info, after, 0, 0, "write", false, false, false)
			t.recordChatFileVersion(execCtx, plan.FilePath, snap, "write", false)
		}
	}
	appendAccessPolicyMetadata(payload, access)
	t.appendFileChangeHookResults(ctx, execCtx, payload, FileChangeEvent{
		WorkspaceRoot: fileChangeWorkspaceRoot(execCtx),
		FilePath:      plan.FilePath,
		Operation:     "write",
		ContentSHA256: after,
		Content:       append([]byte(nil), writeBytes...),
		LineStats:     lineStats,
	})
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) invokeEdit(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	accessCfg := t.sessionFileToolsConfig(filetools.WriteAccess, execCtx)
	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, accessPolicySessionWithFallback(execCtx, accessCfg.WorkingDirectory), filetools.WriteAccess, stringArg(args, "file_path"))
	if err != nil {
		return fileToolError("file_edit_invalid_plan", err.Error()), nil
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
	requiresWriteApproval := t.cfg.FileTools.RequireWriteApproval && !writeAllowedBySessionHostAccess(execCtx, plan.FilePath) && !writeAllowedBySessionWorkspace(execCtx, plan.FilePath) && !writeAutoApprovedByAccessLevel(access)
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
	if t.cfg.FileTools.RequireReadBeforeWrite {
		if result, ok := t.validateReadBeforeFileMutation(plan.FilePath, execCtx, "file_edit"); ok {
			return result, nil
		}
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
		return fileToolError("file_edit_file_not_found", "file does not exist and old_string is not empty"), nil
	}
	if strings.TrimSpace(currentEncoding) == "" {
		currentEncoding = "utf-8"
	}

	normalizedContent, lineEndings := normalizeEditLineEndings(currentContent)
	oldString := normalizeEditString(plan.OldString)
	newString := normalizeEditString(plan.NewString)

	updatedContent := ""
	replacements := 0
	if oldString == "" {
		if beforeExists && normalizedContent != "" {
			return fileToolError("file_edit_file_exists", "old_string is empty but file already has content"), nil
		}
		updatedContent = newString
		replacements = 1
	} else {
		replacements = strings.Count(normalizedContent, oldString)
		if replacements == 0 {
			return fileEditStringNotFoundResult(normalizedContent, oldString, newString), nil
		}
		if replacements > 1 && !plan.ReplaceAll {
			return fileToolError("file_edit_multiple_matches", fmt.Sprintf("old_string matched %d times; set replace_all=true or provide more context", replacements)), nil
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
	after := fileSHA256(plan.FilePath)
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
		if execCtx != nil {
			snap := recordReadSnapshot(execCtx, plan.FilePath, info, after, 0, 0, "edit", false, false, false)
			t.recordChatFileVersion(execCtx, plan.FilePath, snap, "edit", false)
		}
	}
	appendAccessPolicyMetadata(payload, access)
	t.appendFileChangeHookResults(ctx, execCtx, payload, FileChangeEvent{
		WorkspaceRoot: fileChangeWorkspaceRoot(execCtx),
		FilePath:      plan.FilePath,
		Operation:     "edit",
		ContentSHA256: after,
		Content:       append([]byte(nil), updatedBytes...),
		LineStats:     lineStats,
	})
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) appendFileChangeHookResults(ctx context.Context, execCtx *ExecutionContext, payload map[string]any, event FileChangeEvent) {
	if t == nil || len(t.fileChangeHooks) == 0 || execCtx == nil {
		return
	}
	if !execCtx.Session.ModeCapabilities.FileChangeHooks {
		return
	}
	if strings.TrimSpace(event.WorkspaceRoot) == "" {
		return
	}
	results := make([]FileChangeHookResult, 0, len(t.fileChangeHooks))
	for _, hook := range t.fileChangeHooks {
		if hook == nil {
			continue
		}
		results = append(results, hook.AfterFileChange(ctx, event))
	}
	if len(results) > 0 {
		payload["hooks"] = results
	}
}

func fileChangeWorkspaceRoot(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	return filetools.SessionWorkspaceRoot(execCtx.Session)
}

func (t *RuntimeToolExecutor) sessionFileToolsConfig(mode filetools.AccessMode, execCtx *ExecutionContext) config.FileToolsConfig {
	cfg := t.cfg.FileTools
	if execCtx != nil {
		if workspaceRoot := filetools.SessionWorkspaceRoot(execCtx.Session); workspaceRoot != "" {
			cfg.WorkingDirectory = workspaceRoot
		}
	}
	return cfg
}

func writeAllowedBySessionWorkspace(execCtx *ExecutionContext, path string) bool {
	if execCtx == nil {
		return false
	}
	return filetools.PathInSessionWorkspace(execCtx.Session, path)
}

func writeAllowedBySessionHostAccess(execCtx *ExecutionContext, path string) bool {
	if execCtx == nil {
		return false
	}
	return filetools.PathInSessionHostWriteRoot(execCtx.Session, path)
}

func accessPolicySessionWithFallback(execCtx *ExecutionContext, fallbackWorkspace string) QuerySession {
	fallbackWorkspace = strings.TrimSpace(fallbackWorkspace)
	if execCtx == nil {
		return QuerySession{WorkspaceRoot: fallbackWorkspace}
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
	if strings.TrimSpace(session.WorkspaceRoot) == "" && strings.TrimSpace(session.RuntimeContext.LocalPaths.WorkspaceDir) == "" && fallbackWorkspace != "" {
		session.WorkspaceRoot = fallbackWorkspace
		session.RuntimeContext.LocalPaths.WorkspaceDir = fallbackWorkspace
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

func (t *RuntimeToolExecutor) validateReadBeforeWrite(path string, execCtx *ExecutionContext) (ToolExecutionResult, bool) {
	return t.validateReadBeforeFileMutation(path, execCtx, "file_write")
}

func (t *RuntimeToolExecutor) validateReadBeforeFileMutation(path string, execCtx *ExecutionContext, errorPrefix string) (ToolExecutionResult, bool) {
	notReadCode := errorPrefix + "_not_read"
	modifiedCode := errorPrefix + "_modified_since_read"
	partialCode := errorPrefix + "_partial_read"
	if errorPrefix == "file_write" {
		modifiedCode = "file_modified_since_read"
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ToolExecutionResult{}, false
		}
		return fileToolError(errorPrefix+"_failed", err.Error()), true
	}
	if execCtx == nil || execCtx.ReadFileState == nil {
		if snap, ok := t.loadChatFileVersionSnapshot(execCtx, path); ok {
			if result, rejected := validateFileSnapshot(path, info, snap, modifiedCode, partialCode, execCtx, true); rejected {
				return result, true
			}
			return ToolExecutionResult{}, false
		}
		return fileToolError(notReadCode, "file exists but was not fully read in this run; call file_read without offset/limit and ensure truncated=false before retrying"), true
	}
	snap, ok := execCtx.ReadFileState[path]
	if !ok {
		if chatSnap, chatOK := t.loadChatFileVersionSnapshot(execCtx, path); chatOK {
			if result, rejected := validateFileSnapshot(path, info, chatSnap, modifiedCode, partialCode, execCtx, true); rejected {
				return result, true
			}
			return ToolExecutionResult{}, false
		}
		return fileToolError(notReadCode, "file exists but was not fully read in this run; call file_read without offset/limit and ensure truncated=false before retrying"), true
	}
	return validateFileSnapshot(path, info, snap, modifiedCode, partialCode, nil, false)
}

func validateFileSnapshot(path string, info os.FileInfo, snap ReadFileSnapshot, modifiedCode string, partialCode string, restoreCtx *ExecutionContext, forceSHA bool) (ToolExecutionResult, bool) {
	statChanged := info.ModTime().UnixMilli() != snap.ModifiedUnixMs || info.Size() != snap.SizeBytes
	if forceSHA || statChanged {
		currentSha := fileSHA256(path)
		if currentSha != snap.SHA256 {
			return fileToolError(modifiedCode, "file has been modified since last read; re-read before writing"), true
		}
	}
	if snapshotBlocksMutation(snap) {
		return fileToolError(partialCode, "file was not fully read; call file_read without offset/limit and ensure truncated=false before retrying"), true
	}
	if statChanged {
		snap.ModifiedUnixMs = info.ModTime().UnixMilli()
		snap.SizeBytes = info.Size()
	}
	if restoreCtx != nil {
		if restoreCtx.ReadFileState == nil {
			restoreCtx.ReadFileState = map[string]ReadFileSnapshot{}
		}
		restoreCtx.ReadFileState[path] = snap
	}
	return ToolExecutionResult{}, false
}

func snapshotBlocksMutation(snap ReadFileSnapshot) bool {
	if snap.Partial || snap.Truncated {
		return true
	}
	return snap.Source == "read" && (snap.Offset > 1 || snap.Limit > 0)
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
	return fileToolErrorWithFields("file_edit_string_not_found", "old_string was not found in file. If you copied from file_read output with line numbers, the tab after the line number is not part of the file content; re-read with add_line_numbers=false and retry.", map[string]any{
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

func recordReadSnapshot(execCtx *ExecutionContext, path string, info os.FileInfo, sha string, offset int64, limit int64, source string, lineNumbered bool, partial bool, truncated bool) ReadFileSnapshot {
	if execCtx == nil {
		return ReadFileSnapshot{}
	}
	if execCtx.ReadFileState == nil {
		execCtx.ReadFileState = map[string]ReadFileSnapshot{}
	}
	snap := ReadFileSnapshot{
		ModifiedUnixMs: info.ModTime().UnixMilli(),
		SizeBytes:      info.Size(),
		SHA256:         sha,
		Offset:         offset,
		Limit:          limit,
		ReadAtUnixMs:   time.Now().UnixMilli(),
		Source:         source,
		LineNumbered:   lineNumbered,
		Partial:        partial,
		Truncated:      truncated,
	}
	execCtx.ReadFileState[path] = snap
	return snap
}

func addLineNumbersArg(args map[string]any) bool {
	if _, ok := args["add_line_numbers"]; !ok {
		return false
	}
	return boolArg(args, "add_line_numbers")
}

func addLineNumbers(content string, startLine int) string {
	if content == "" {
		return ""
	}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	maxLine := startLine + len(lines) - 1
	width := len(fmt.Sprintf("%d", maxLine))
	if width < 6 {
		width = 6
	}
	var builder strings.Builder
	for idx, line := range lines {
		builder.WriteString(fmt.Sprintf("%*d\t%s", width, startLine+idx, line))
	}
	return builder.String()
}

func fileToolError(code string, message string) ToolExecutionResult {
	return fileToolErrorWithFields(code, message, nil)
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

func readLineRangeLimited(file *os.File, startLine int, limit int, maxBytes int) ([]byte, bool, error) {
	if startLine < 1 {
		startLine = 1
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	reader := bufio.NewReader(file)
	var builder bytes.Buffer
	lineNumber := 1
	capturedLines := 0
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if lineNumber >= startLine && (limit <= 0 || capturedLines < limit) {
				remaining := maxBytes - builder.Len()
				if remaining <= 0 {
					return builder.Bytes(), true, nil
				}
				if len(line) > remaining {
					builder.Write(line[:remaining])
					return builder.Bytes(), true, nil
				}
				builder.Write(line)
				capturedLines++
				if limit > 0 && capturedLines >= limit {
					return builder.Bytes(), false, nil
				}
			}
			lineNumber++
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return builder.Bytes(), false, nil
			}
			return nil, false, err
		}
	}
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
