package tools

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/filetools"
)

func (t *RuntimeToolExecutor) invokeRead(args map[string]any) (ToolExecutionResult, error) {
	resolved, err := filetools.ResolvePath(t.cfg.FileTools, filetools.ReadAccess, stringArg(args, "file_path"))
	if err != nil {
		return fileToolError("file_read_denied", err.Error()), nil
	}
	info, err := os.Stat(resolved.Path)
	if err != nil {
		return fileToolError("file_read_failed", err.Error()), nil
	}
	if info.IsDir() {
		return fileToolError("file_read_is_directory", "path is a directory"), nil
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

	offset := int64Arg(args, "offset")
	limit := int64Arg(args, "limit")
	startLine := 1
	if offset > 0 {
		startLine = int(offset)
	}
	lineLimited := offset > 0 || limit > 0
	if lineLimited && utf8.Valid(data) {
		data = selectLineRange(data, startLine, int(limit))
	}

	payload := map[string]any{
		"filePath":       resolved.Path,
		"sizeBytes":      info.Size(),
		"readBytes":      len(data),
		"sha256":         fileSHA256(resolved.Path),
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
		payload["encoding"] = "utf-8"
		payload["content"] = string(data)
	} else {
		payload["encoding"] = "base64"
		payload["contentBase64"] = base64.StdEncoding.EncodeToString(data)
	}
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) invokeWrite(args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
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
	}
	return structuredResult(payload), nil
}

func fileToolError(code string, message string) ToolExecutionResult {
	result := structuredResultWithExit(map[string]any{
		"error":   code,
		"message": strings.TrimSpace(message),
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
