package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"agent-platform-runner-go/internal/config"
	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/filetools"
)

func (t *RuntimeToolExecutor) invokeRead(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	accessCfg := t.sessionFileToolsConfig(filetools.ReadAccess, execCtx)
	access, err := filetools.BuildAccessPlan(accessCfg, filetools.ReadAccess, stringArg(args, "file_path"))
	if err != nil {
		return fileToolError("file_read_invalid_path", err.Error()), nil
	}
	if filetools.IsBlockedDeviceFile(access.Path) {
		return fileToolError("file_read_device_blocked", "device file is blocked"), nil
	}
	if !access.AllowedByWhitelist && !filetools.ConsumeReadApproval(execCtx, access) {
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
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) invokeWrite(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	access, err := filetools.BuildAccessPlan(t.cfg.FileTools, filetools.WriteAccess, stringArg(args, "file_path"))
	if err != nil {
		return fileToolError("file_write_invalid_plan", err.Error()), nil
	}
	if !access.AllowedByWhitelist && !filetools.ConsumeAccessApproval(execCtx, access) {
		return fileAccessApprovalRequired("file_write_path_approval_required", "write超出允许目录", access), nil
	}
	plan, err := filetools.BuildWritePlan(t.cfg.FileTools, args)
	if err != nil {
		return fileToolError("file_write_invalid_plan", err.Error()), nil
	}
	if t.cfg.FileTools.RequireWriteApproval && !filetools.ConsumeWriteApproval(execCtx, plan) {
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
	if err := os.MkdirAll(filepath.Dir(plan.FilePath), 0o755); err != nil {
		return fileToolError("file_write_failed", err.Error()), nil
	}
	if err := atomicWriteFile(plan.FilePath, plan.Content); err != nil {
		return fileToolError("file_write_failed", err.Error()), nil
	}
	after := fileSHA256(plan.FilePath)
	info, _ := os.Stat(plan.FilePath)
	payload := map[string]any{
		"status":       "written",
		"filePath":     plan.FilePath,
		"bytesWritten": len(plan.Content),
		"created":      !beforeExists,
		"overwritten":  beforeExists,
		"sha256":       after,
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
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) sessionFileToolsConfig(mode filetools.AccessMode, execCtx *ExecutionContext) config.FileToolsConfig {
	if execCtx == nil {
		return t.cfg.FileTools
	}
	return filetools.ConfigWithSessionReadRoots(t.cfg.FileTools, mode, execCtx.Session)
}

func readImageFile(path string, info os.FileInfo) (map[string]any, bool, ToolExecutionResult) {
	if !filetools.IsSupportedImageExtension(path) {
		return nil, false, ToolExecutionResult{}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, true, fileToolError("file_read_failed", err.Error())
	}
	defer file.Close()

	header := make([]byte, 512)
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, true, fileToolError("file_read_failed", err.Error())
	}
	mime := http.DetectContentType(header[:n])
	if !filetools.IsSupportedImageMime(mime) {
		return nil, false, ToolExecutionResult{}
	}
	if info.Size() > filetools.MaxInlineImageBytes {
		return nil, true, fileToolError("file_read_image_too_large", fmt.Sprintf("image exceeds max inline bytes: %d", filetools.MaxInlineImageBytes))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, true, fileToolError("file_read_failed", err.Error())
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, true, fileToolError("file_read_failed", err.Error())
	}
	sha := sha256BytesHex(data)
	return map[string]any{
		"filePath":       path,
		"kind":           "image",
		"mimeType":       mime,
		"sizeBytes":      info.Size(),
		"modifiedUnixMs": info.ModTime().UnixMilli(),
		"sha256":         sha,
		"contentBase64":  base64.StdEncoding.EncodeToString(data),
	}, true, ToolExecutionResult{}
}

func validateReadBeforeWrite(path string, execCtx *ExecutionContext) (ToolExecutionResult, bool) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ToolExecutionResult{}, false
		}
		return fileToolError("file_write_failed", err.Error()), true
	}
	if execCtx == nil || execCtx.ReadFileState == nil {
		return fileToolError("file_write_not_read", "file exists but was not read in this run; call read first then retry"), true
	}
	snap, ok := execCtx.ReadFileState[path]
	if !ok {
		return fileToolError("file_write_not_read", "file exists but was not read in this run; call read first then retry"), true
	}
	if info.ModTime().UnixMilli() != snap.ModifiedUnixMs || info.Size() != snap.SizeBytes {
		if currentSha := fileSHA256(path); currentSha != snap.SHA256 {
			return fileToolError("file_modified_since_read", "file has been modified since last read; re-read before writing"), true
		}
	}
	return ToolExecutionResult{}, false
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
