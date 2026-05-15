package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	. "agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/filetools"
)

const (
	defaultGrepHeadLimit = 250
	maxGrepRawBytes      = 8 * 1024
)

func (t *RuntimeToolExecutor) invokeGrep(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	pattern := stringArg(args, "pattern")
	if strings.TrimSpace(pattern) == "" {
		return fileToolError("grep_invalid_pattern", "pattern is required"), nil
	}
	rawPath := strings.TrimSpace(stringArg(args, "path"))
	if rawPath == "" {
		rawPath = "."
	}
	accessCfg := t.sessionFileToolsConfig(filetools.ReadAccess, execCtx)
	access, err := filetools.BuildAccessPlan(accessCfg, filetools.ReadAccess, rawPath)
	if err != nil {
		return fileToolError("grep_invalid_path", err.Error()), nil
	}
	if filetools.IsBlockedDeviceFile(access.Path) {
		return fileToolError("file_read_device_blocked", "device file is blocked"), nil
	}
	if !access.AllowedByWhitelist && !filetools.ConsumeReadApproval(execCtx, access) {
		return fileAccessApprovalRequired("file_read_approval_required", "grep超出允许目录", access), nil
	}
	resolved := filetools.ResolvedPath{Raw: access.RawPath, Path: access.Path, Root: access.Root}
	rgPath, err := exec.LookPath("rg")
	if err != nil {
		return fileToolError("grep_ripgrep_missing", "ripgrep (rg) is not installed or not on PATH"), nil
	}

	mode := strings.ToLower(strings.TrimSpace(stringArg(args, "output_mode")))
	if mode == "" {
		mode = "files_with_matches"
	}
	if mode != "content" && mode != "files_with_matches" && mode != "count" {
		return fileToolError("grep_invalid_mode", "output_mode must be content, files_with_matches, or count"), nil
	}

	rgArgs := []string{
		"--no-config",
		"--color", "never",
		"--hidden",
		"--no-messages",
		"--max-columns", "500",
		"--glob", "!.git",
		"--glob", "!.svn",
		"--glob", "!.hg",
		"--glob", "!.bzr",
		"--glob", "!.jj",
		"--glob", "!.sl",
	}
	switch mode {
	case "files_with_matches":
		rgArgs = append(rgArgs, "-l")
	case "count":
		rgArgs = append(rgArgs, "-c")
	case "content":
		if _, ok := args["-n"]; !ok || boolArg(args, "-n") {
			rgArgs = append(rgArgs, "-n")
		}
	}
	if boolArg(args, "-i") {
		rgArgs = append(rgArgs, "-i")
	}
	for _, flag := range []string{"-A", "-B", "-C"} {
		if value := int64Arg(args, flag); value > 0 {
			rgArgs = append(rgArgs, flag, formatInt64(value))
		}
	}
	if boolArg(args, "multiline") {
		rgArgs = append(rgArgs, "-U", "--multiline-dotall")
	}
	if glob := strings.TrimSpace(stringArg(args, "glob")); glob != "" {
		rgArgs = append(rgArgs, "--glob", glob)
	}
	if typ := strings.TrimSpace(stringArg(args, "type")); typ != "" {
		rgArgs = append(rgArgs, "--type", typ)
	}
	if strings.HasPrefix(pattern, "-") {
		rgArgs = append(rgArgs, "-e", pattern)
	} else {
		rgArgs = append(rgArgs, pattern)
	}
	rgArgs = append(rgArgs, resolved.Path)

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
	out := stdout.String()
	errText := stderr.String()
	if err != nil && strings.TrimSpace(out) == "" {
		if exitCode == 1 {
			return fileToolError("grep_no_match", "no matches found"), nil
		}
		if strings.Contains(errText, "unrecognized file type") || strings.Contains(errText, "unknown file type") {
			return fileToolError("grep_invalid_type", strings.TrimSpace(errText)), nil
		}
		if ctx.Err() != nil {
			return fileToolError("grep_failed", ctx.Err().Error()), nil
		}
		if strings.TrimSpace(errText) == "" {
			errText = err.Error()
		}
		return fileToolError("grep_failed", errText), nil
	}

	lines := splitOutputLines(out)
	if mode == "files_with_matches" {
		sortGrepFiles(lines)
	}
	offset := numericArg(args, "offset")
	if offset < 0 {
		offset = 0
	}
	headLimit := numericArg(args, "head_limit")
	if _, ok := args["head_limit"]; !ok {
		headLimit = defaultGrepHeadLimit
	}
	results, truncated := pageGrepResults(lines, offset, headLimit)
	return structuredResult(map[string]any{
		"tool":       "grep",
		"mode":       mode,
		"pattern":    pattern,
		"path":       resolved.Path,
		"matchCount": len(lines),
		"truncated":  truncated,
		"offset":     offset,
		"headLimit":  headLimit,
		"results":    results,
		"raw":        truncateStringBytes(out, maxGrepRawBytes),
	}), nil
}

func splitOutputLines(out string) []string {
	out = strings.TrimRight(out, "\r\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func pageGrepResults(lines []string, offset int, headLimit int) ([]string, bool) {
	if offset >= len(lines) {
		return []string{}, false
	}
	paged := lines[offset:]
	if headLimit > 0 && len(paged) > headLimit {
		return append([]string(nil), paged[:headLimit]...), true
	}
	return append([]string(nil), paged...), false
}

func sortGrepFiles(lines []string) {
	sort.SliceStable(lines, func(i, j int) bool {
		left, leftErr := os.Stat(lines[i])
		right, rightErr := os.Stat(lines[j])
		if leftErr != nil || rightErr != nil {
			return lines[i] < lines[j]
		}
		if left.ModTime().Equal(right.ModTime()) {
			return lines[i] < lines[j]
		}
		return left.ModTime().After(right.ModTime())
	})
}

func truncateStringBytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes]
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

func numericArg(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	case json.Number:
		n, _ := value.Int64()
		return int(n)
	default:
		return 0
	}
}
