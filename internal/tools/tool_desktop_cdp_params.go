package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
)

func (t *RuntimeToolExecutor) resolveDesktopCDPParams(args map[string]any, execCtx *ExecutionContext) (map[string]any, ToolExecutionResult, bool) {
	rawFile, hasFile := args["paramsFile"]
	if !hasFile {
		raw, present := args["params"]
		params, valid := raw.(map[string]any)
		if present && (!valid || params == nil) {
			return nil, desktopActionErrorResult("invalid_args", "params must be a JSON object; omit params for a method with no parameters. The request was not sent.", map[string]any{
				"executed": false, "retryable": false, "recovery": "Replace params with a JSON object; do not retry the same input.",
			}), true
		}
		if !present {
			params = map[string]any{}
		}
		return params, ToolExecutionResult{}, false
	}
	if _, hasParams := args["params"]; hasParams {
		return nil, desktopActionErrorResult("invalid_args", "params and paramsFile are mutually exclusive", nil), true
	}
	path, ok := rawFile.(string)
	if !ok || strings.TrimSpace(path) == "" {
		return nil, desktopActionErrorResult("invalid_args", "paramsFile must be a non-empty path string", nil), true
	}
	data, failure, failed := t.readDesktopInputFile("paramsFile", "desktop_cdp_params_file", path, execCtx)
	if failed {
		return nil, failure, true
	}
	var params map[string]any
	if !utf8.Valid(data) || json.Unmarshal(data, &params) != nil || params == nil {
		return nil, desktopActionErrorResult("desktop_cdp_params_file_invalid_json", "paramsFile must contain a single UTF-8 JSON object containing only the CDP params", nil), true
	}
	return params, ToolExecutionResult{}, false
}

// readDesktopInputFile reads one model-supplied input file in full under the
// standard file read AccessPolicy and approval rules.
func (t *RuntimeToolExecutor) readDesktopInputFile(field, codePrefix, path string, execCtx *ExecutionContext) ([]byte, ToolExecutionResult, bool) {
	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, t.policySession(execCtx), filetools.ReadAccess, path)
	if err != nil {
		return nil, filePathResolutionError(codePrefix+"_invalid_path", err), true
	}
	if access.Blocked {
		return nil, desktopActionErrorResult(codePrefix+"_path_blocked", access.Reason, nil), true
	}
	if filetools.IsBlockedDeviceFile(access.Path) {
		return nil, desktopActionErrorResult(codePrefix+"_device_blocked", "device file is blocked", nil), true
	}
	if !access.AllowedByWhitelist && !access.AutoApproved && !filetools.ConsumeReadApproval(execCtx, access) {
		return nil, fileAccessApprovalRequired(codePrefix+"_approval_required", field+" read超出允许目录", access), true
	}

	// Reject special files before opening: a FIFO must not block the tool loop.
	info, err := os.Stat(access.Path)
	if err != nil {
		return nil, desktopActionErrorResult(codePrefix+"_read_failed", err.Error(), nil), true
	}
	if !info.Mode().IsRegular() {
		return nil, desktopActionErrorResult(codePrefix+"_invalid_file", field+" must be a regular file", nil), true
	}
	maxBytes := t.cfg.FileTools.MaxReadBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	tooLarge := func() ([]byte, ToolExecutionResult, bool) {
		return nil, desktopActionErrorResult(codePrefix+"_too_large", fmt.Sprintf("%s exceeds max read bytes (%d)", field, maxBytes), nil), true
	}
	if info.Size() > int64(maxBytes) {
		return tooLarge()
	}
	file, err := os.Open(access.Path)
	if err != nil {
		return nil, desktopActionErrorResult(codePrefix+"_read_failed", err.Error(), nil), true
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, desktopActionErrorResult(codePrefix+"_read_failed", err.Error(), nil), true
	}
	if len(data) > maxBytes {
		return tooLarge()
	}
	return data, ToolExecutionResult{}, false
}
