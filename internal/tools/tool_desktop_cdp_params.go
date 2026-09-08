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
		params, _ := args["params"].(map[string]any)
		if params == nil {
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

	access, err := filetools.BuildAccessPlanFromPolicy(t.cfg.AccessPolicy, accessPolicySession(execCtx), filetools.ReadAccess, path)
	if err != nil {
		return nil, filePathResolutionError("desktop_cdp_params_file_invalid_path", err), true
	}
	if access.Blocked {
		return nil, desktopActionErrorResult("desktop_cdp_params_file_path_blocked", access.Reason, nil), true
	}
	if filetools.IsBlockedDeviceFile(access.Path) {
		return nil, desktopActionErrorResult("desktop_cdp_params_file_device_blocked", "device file is blocked", nil), true
	}
	if !access.AllowedByWhitelist && !access.AutoApproved && !filetools.ConsumeReadApproval(execCtx, access) {
		return nil, fileAccessApprovalRequired("desktop_cdp_params_file_approval_required", "paramsFile read超出允许目录", access), true
	}

	// Reject special files before opening: a FIFO must not block the tool loop.
	info, err := os.Stat(access.Path)
	if err != nil {
		return nil, desktopActionErrorResult("desktop_cdp_params_file_read_failed", err.Error(), nil), true
	}
	if !info.Mode().IsRegular() {
		return nil, desktopActionErrorResult("desktop_cdp_params_file_invalid_file", "paramsFile must be a regular file", nil), true
	}
	maxBytes := t.cfg.FileTools.MaxReadBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	tooLarge := func() (map[string]any, ToolExecutionResult, bool) {
		return nil, desktopActionErrorResult("desktop_cdp_params_file_too_large", fmt.Sprintf("paramsFile exceeds max read bytes (%d)", maxBytes), nil), true
	}
	if info.Size() > int64(maxBytes) {
		return tooLarge()
	}
	file, err := os.Open(access.Path)
	if err != nil {
		return nil, desktopActionErrorResult("desktop_cdp_params_file_read_failed", err.Error(), nil), true
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, desktopActionErrorResult("desktop_cdp_params_file_read_failed", err.Error(), nil), true
	}
	if len(data) > maxBytes {
		return tooLarge()
	}
	var params map[string]any
	if !utf8.Valid(data) || json.Unmarshal(data, &params) != nil || params == nil {
		return nil, desktopActionErrorResult("desktop_cdp_params_file_invalid_json", "paramsFile must contain a single UTF-8 JSON object containing only the CDP params", nil), true
	}
	return params, ToolExecutionResult{}, false
}
