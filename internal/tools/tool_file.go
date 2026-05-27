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
	"agent-platform/internal/filetools"
	"agent-platform/internal/multimodal"
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
	snapshotOffset := int64(0)
	if offset > 0 {
		snapshotOffset = offset
	}
	snapshotLimit := int64(0)
	if limit > 0 {
		snapshotLimit = limit
	}
	if execCtx != nil {
		if execCtx.ReadFileState == nil {
			execCtx.ReadFileState = map[string]ReadFileSnapshot{}
		}
		if snap, ok := execCtx.ReadFileState[resolved.Path]; ok &&
			snap.ModifiedUnixMs == info.ModTime().UnixMilli() &&
			snap.SizeBytes == info.Size() &&
			snap.Offset == snapshotOffset &&
			snap.Limit == snapshotLimit {
			return structuredResult(map[string]any{
				"filePath":       resolved.Path,
				"kind":           "unchanged",
				"modifiedUnixMs": int64(0),
				"sizeBytes":      int64(0),
				"message":        "File unchanged since last read. Refer to the earlier read result in this conversation.",
			}), nil
		}
	}
	if image, handled, result := readImageFile(resolved.Path, info); handled {
		if result.ExitCode != 0 || result.Error != "" {
			return result, nil
		}
		if execCtx != nil {
			recordReadSnapshot(execCtx, resolved.Path, info, image["sha256"].(string), snapshotOffset, snapshotLimit)
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

	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return fileToolError("file_read_failed", err.Error()), nil
	}
	truncated := len(data) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}

	startLine := 1
	if offset > 0 {
		startLine = int(offset)
	}
	lineLimited := offset > 0 || limit > 0
	if lineLimited && utf8.Valid(data) {
		data = selectLineRange(data, startLine, int(limit))
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
	if utf8.Valid(data) {
		content := string(data)
		payload["encoding"] = "utf-8"
		payload["kind"] = "text"
		if addLineNumbersArg(args) {
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
		recordReadSnapshot(execCtx, resolved.Path, info, sha, snapshotOffset, snapshotLimit)
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
	requiresWriteApproval := t.cfg.FileTools.RequireWriteApproval && !writeAllowedBySessionWorkspace(execCtx, plan.FilePath) && !writeAutoApprovedByAccessLevel(access)
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
		if result, ok := validateReadBeforeWrite(plan.FilePath, execCtx); ok {
			return result, nil
		}
	}
	before, beforeExists := fileSHA256IfExists(plan.FilePath)
	beforeContent := ""
	if beforeExists {
		data, err := os.ReadFile(plan.FilePath)
		if err != nil {
			return fileToolError("file_write_failed", err.Error()), nil
		}
		beforeContent = string(data)
	}
	if err := os.MkdirAll(filepath.Dir(plan.FilePath), 0o755); err != nil {
		return fileToolError("file_write_failed", err.Error()), nil
	}
	if err := atomicWriteFile(plan.FilePath, plan.Content); err != nil {
		return fileToolError("file_write_failed", err.Error()), nil
	}
	after := fileSHA256(plan.FilePath)
	info, _ := os.Stat(plan.FilePath)
	lineStats := computeLineDiffStats(beforeContent, string(plan.Content))
	payload := map[string]any{
		"status":       "written",
		"filePath":     plan.FilePath,
		"bytesWritten": len(plan.Content),
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
			recordReadSnapshot(execCtx, plan.FilePath, info, after, 0, 0)
		}
	}
	appendAccessPolicyMetadata(payload, access)
	t.appendFileChangeHookResults(ctx, execCtx, payload, FileChangeEvent{
		WorkspaceRoot: fileChangeWorkspaceRoot(execCtx),
		FilePath:      plan.FilePath,
		Operation:     "write",
		ContentSHA256: after,
		Content:       append([]byte(nil), plan.Content...),
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
	requiresWriteApproval := t.cfg.FileTools.RequireWriteApproval && !writeAllowedBySessionWorkspace(execCtx, plan.FilePath) && !writeAutoApprovedByAccessLevel(access)
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
		if result, ok := validateReadBeforeFileMutation(plan.FilePath, execCtx, "file_edit"); ok {
			return result, nil
		}
	}

	before, beforeExists := fileSHA256IfExists(plan.FilePath)
	currentContent := ""
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
		if !utf8.Valid(data) {
			return fileToolError("file_edit_non_utf8_unsupported", "file content is not valid UTF-8"), nil
		}
		currentContent = string(data)
	} else if plan.OldString != "" {
		return fileToolError("file_edit_file_not_found", "file does not exist and old_string is not empty"), nil
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
			return fileToolError("file_edit_string_not_found", "old_string was not found in file"), nil
		}
		if replacements > 1 && !plan.ReplaceAll {
			return fileToolError("file_edit_multiple_matches", fmt.Sprintf("old_string matched %d times; set replace_all=true or provide more context", replacements)), nil
		}
		if plan.ReplaceAll {
			updatedContent = strings.ReplaceAll(normalizedContent, oldString, newString)
		} else {
			updatedContent = strings.Replace(normalizedContent, oldString, newString, 1)
			replacements = 1
		}
	}

	if lineEndings == "CRLF" {
		updatedContent = strings.ReplaceAll(updatedContent, "\n", "\r\n")
	}
	updatedBytes := []byte(updatedContent)
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
	lineStats := computeLineDiffStats(currentContent, string(updatedBytes))
	payload := map[string]any{
		"status":       "edited",
		"filePath":     plan.FilePath,
		"replacements": replacements,
		"replaceAll":   plan.ReplaceAll,
		"created":      created,
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
			recordReadSnapshot(execCtx, plan.FilePath, info, after, 0, 0)
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
	if !strings.EqualFold(strings.TrimSpace(execCtx.Session.Mode), "CODER") {
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
	if execCtx == nil {
		return t.cfg.FileTools
	}
	if mode == filetools.WriteAccess {
		return filetools.ConfigWithSessionWriteRoots(t.cfg.FileTools, execCtx.Session)
	}
	return filetools.ConfigWithSessionReadRoots(t.cfg.FileTools, mode, execCtx.Session)
}

func writeAllowedBySessionWorkspace(execCtx *ExecutionContext, path string) bool {
	if execCtx == nil {
		return false
	}
	return filetools.PathInSessionWorkspace(execCtx.Session, path)
}

func accessPolicySession(execCtx *ExecutionContext) QuerySession {
	return accessPolicySessionWithFallback(execCtx, "")
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

func validateReadBeforeWrite(path string, execCtx *ExecutionContext) (ToolExecutionResult, bool) {
	return validateReadBeforeFileMutation(path, execCtx, "file_write")
}

func validateReadBeforeFileMutation(path string, execCtx *ExecutionContext, errorPrefix string) (ToolExecutionResult, bool) {
	notReadCode := errorPrefix + "_not_read"
	modifiedCode := errorPrefix + "_modified_since_read"
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
		return fileToolError(notReadCode, "file exists but was not read in this run; call read first then retry"), true
	}
	snap, ok := execCtx.ReadFileState[path]
	if !ok {
		return fileToolError(notReadCode, "file exists but was not read in this run; call read first then retry"), true
	}
	if info.ModTime().UnixMilli() != snap.ModifiedUnixMs || info.Size() != snap.SizeBytes {
		if currentSha := fileSHA256(path); currentSha != snap.SHA256 {
			return fileToolError(modifiedCode, "file has been modified since last read; re-read before writing"), true
		}
	}
	return ToolExecutionResult{}, false
}

func normalizeEditLineEndings(content string) (string, string) {
	if strings.Contains(content, "\r\n") {
		return strings.ReplaceAll(content, "\r\n", "\n"), "CRLF"
	}
	return content, "LF"
}

func normalizeEditString(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

func recordReadSnapshot(execCtx *ExecutionContext, path string, info os.FileInfo, sha string, offset int64, limit int64) {
	if execCtx == nil {
		return
	}
	if execCtx.ReadFileState == nil {
		execCtx.ReadFileState = map[string]ReadFileSnapshot{}
	}
	execCtx.ReadFileState[path] = ReadFileSnapshot{
		ModifiedUnixMs: info.ModTime().UnixMilli(),
		SizeBytes:      info.Size(),
		SHA256:         sha,
		Offset:         offset,
		Limit:          limit,
		ReadAtUnixMs:   time.Now().UnixMilli(),
	}
}

func addLineNumbersArg(args map[string]any) bool {
	if _, ok := args["add_line_numbers"]; !ok {
		return true
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
	result := structuredResultWithExit(map[string]any{
		"error":   code,
		"message": strings.TrimSpace(message),
	}, -1)
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

func selectLineRange(data []byte, startLine int, limit int) []byte {
	if startLine <= 1 && limit <= 0 {
		return data
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if startLine < 1 {
		startLine = 1
	}
	start := startLine - 1
	if start >= len(lines) {
		return nil
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return bytes.Join(lines[start:end], nil)
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

func sha256BytesHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
