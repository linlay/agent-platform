package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/pathutil"
)

var searchGlobSemanticRoots = []string{
	"@workspace",
	"@chat",
	"@agent",
	"@skills",
	"@skills-center",
	"@owner",
	"@temp",
}

func validateRelativeSearchGlob(value string, field string) error {
	if !isRootQualifiedSearchGlob(value) {
		return nil
	}
	return fmt.Errorf("%s must be relative to path; put the search directory in path and use a relative glob such as \"*.yml\" or \"**/agent.yml\"", field)
}

func isRootQualifiedSearchGlob(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return true
	}
	if len(value) >= 3 && isASCIIAlpha(value[0]) && value[1] == ':' && (value[2] == '/' || value[2] == '\\') {
		return true
	}
	if lower == "~" || strings.HasPrefix(lower, "~/") || strings.HasPrefix(lower, `~\`) || strings.HasPrefix(lower, "file://") {
		return true
	}
	for _, root := range searchGlobSemanticRoots {
		if lower == root || strings.HasPrefix(lower, root+"/") || strings.HasPrefix(lower, root+`\`) {
			return true
		}
	}
	return false
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func filesystemRootSearchError(toolName string, code string, path string) (ToolExecutionResult, bool) {
	if !pathutil.IsCanonicalFilesystemRoot(path) {
		return ToolExecutionResult{}, false
	}
	return fileToolErrorWithFields(code, "searching the filesystem root is blocked; pass a narrower path explicitly", map[string]any{
		"tool": toolName,
		"path": path,
	}), true
}

func ripgrepSearchFailure(
	toolName string,
	code string,
	path string,
	rgExitCode int,
	stdout string,
	stderr string,
	fallbackMessage string,
	args map[string]any,
	defaultPartialLimit int,
	sortFiles bool,
	extraFields map[string]any,
) ToolExecutionResult {
	trimmedStderr := strings.TrimSpace(stderr)
	stderrTruncated := len(trimmedStderr) > maxGrepRawBytes
	trimmedStderr = strings.TrimSpace(truncateStringBytes(trimmedStderr, maxGrepRawBytes))
	message := trimmedStderr
	if message == "" {
		message = strings.TrimSpace(fallbackMessage)
	}

	fields := map[string]any{
		"tool":            toolName,
		"path":            path,
		"rgExitCode":      rgExitCode,
		"stderr":          trimmedStderr,
		"stderrTruncated": stderrTruncated,
	}
	for key, value := range extraFields {
		fields[key] = value
	}

	lines := splitOutputLines(stdout)
	if len(lines) > 0 {
		if sortFiles {
			sortGrepFiles(lines)
		}
		partialLimit := defaultPartialLimit
		if requested := numericArg(args, "head_limit"); requested > 0 && requested < partialLimit {
			partialLimit = requested
		}
		partialResults, partialTruncated := pageGrepResults(lines, 0, partialLimit)
		fields["partialResults"] = partialResults
		fields["partialMatchCount"] = len(lines)
		fields["partialTruncated"] = partialTruncated
	}
	return fileToolErrorWithFields(code, message, fields)
}
