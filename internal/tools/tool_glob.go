package tools

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/textcodec"
)

const defaultGlobHeadLimit = 100

func (t *RuntimeToolExecutor) invokeGlob(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	pattern := strings.TrimSpace(stringArg(args, "pattern"))
	if pattern == "" {
		return fileToolError("glob_invalid_pattern", "pattern is required"), nil
	}
	rawPath := strings.TrimSpace(stringArg(args, "path"))
	if rawPath == "" {
		rawPath = "."
	}
	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, accessPolicySessionWithFallback(execCtx, t.cfg.FileTools.WorkingDirectory), filetools.ReadAccess, rawPath)
	if err != nil {
		return fileToolError("glob_invalid_path", err.Error()), nil
	}
	if access.Blocked {
		return fileToolError("glob_path_blocked", access.Reason), nil
	}
	if filetools.IsBlockedDeviceFile(access.Path) {
		return fileToolError("file_read_device_blocked", "device file is blocked"), nil
	}
	if !access.AllowedByWhitelist && !access.AutoApproved && !filetools.ConsumeReadApproval(execCtx, access) {
		return fileAccessApprovalRequired("file_read_approval_required", "glob超出允许目录", access), nil
	}
	resolved := filetools.ResolvedPath{Raw: access.RawPath, Path: access.Path, Root: access.Root}
	info, err := os.Stat(resolved.Path)
	if err != nil {
		return fileToolError("glob_invalid_path", err.Error()), nil
	}
	if !info.IsDir() {
		return fileToolError("glob_invalid_path", "path is not a directory"), nil
	}

	rgPath, err := resolveRipgrepPath()
	if err != nil {
		return fileToolError("glob_ripgrep_missing", "ripgrep (rg) is not installed or bundled with agent-platform"), nil
	}

	rgArgs := []string{
		"--no-config",
		"--color", "never",
		"--files",
		"--hidden",
		"--no-messages",
		"--glob", "!.git",
		"--glob", "!.svn",
		"--glob", "!.hg",
		"--glob", "!.bzr",
		"--glob", "!.jj",
		"--glob", "!.sl",
		"--glob", pattern,
		resolved.Path,
	}
	cmd := exec.CommandContext(ctx, rgPath, rgArgs...)
	cmd.Env = mergeCommandEnv(execCtx)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	runtimeInfo := t.runtimeInfo()
	out := textcodec.DecodeSubprocessOutput(stdout.Bytes(), runtimeInfo)
	errText := textcodec.DecodeSubprocessOutput(stderr.Bytes(), runtimeInfo)
	if err != nil && strings.TrimSpace(out) == "" && exitCode != 1 {
		if strings.Contains(strings.ToLower(errText), "glob") {
			return fileToolError("glob_invalid_pattern", strings.TrimSpace(errText)), nil
		}
		if ctx.Err() != nil {
			return fileToolError("glob_failed", ctx.Err().Error()), nil
		}
		if strings.TrimSpace(errText) == "" {
			errText = err.Error()
		}
		return fileToolError("glob_failed", errText), nil
	}

	lines := splitOutputLines(out)
	sortGrepFiles(lines)
	offset := numericArg(args, "offset")
	if offset < 0 {
		offset = 0
	}
	headLimit := numericArg(args, "head_limit")
	if _, ok := args["head_limit"]; !ok {
		headLimit = defaultGlobHeadLimit
	}
	results, truncated := pageGrepResults(lines, offset, headLimit)
	payload := map[string]any{
		"tool":       "file_glob",
		"pattern":    pattern,
		"path":       resolved.Path,
		"matchCount": len(lines),
		"truncated":  truncated,
		"offset":     offset,
		"headLimit":  headLimit,
		"results":    results,
		"raw":        truncateStringBytes(out, maxGrepRawBytes),
	}
	appendAccessPolicyMetadata(payload, access)
	return structuredResult(payload), nil
}
