package tools

import (
	"encoding/json"
	"errors"
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
	invalidJSON := func(message, actual string, extra map[string]any) (map[string]any, ToolExecutionResult, bool) {
		details := map[string]any{"path": []string{"paramsFile"}, "expectedType": "object", "actualType": actual}
		for key, value := range extra {
			details[key] = value
		}
		return nil, desktopActionErrorResult("desktop_cdp_params_file_invalid_json", message, details), true
	}
	if !utf8.Valid(data) {
		return invalidJSON("paramsFile 不是有效 UTF-8；请将参数文件保存为 UTF-8 JSON 对象。", "invalid UTF-8", nil)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		// Never echo parser messages: an unexpected byte can be credential data.
		offset := int64(len(data) + 1)
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			offset = syntax.Offset
		}
		line, column := desktopParamsJSONPosition(data, offset)
		return invalidJSON(fmt.Sprintf("paramsFile JSON 语法错误（第 %d 行，第 %d 列）；请修正 JSON 语法，仅保留一个完整参数对象。", line, column), "invalid JSON", map[string]any{"line": line, "column": column})
	}
	params, ok := value.(map[string]any)
	if !ok || params == nil {
		return invalidJSON(fmt.Sprintf("paramsFile 根节点必须是 JSON 对象，实际类型为 %s；文件只保存 params 内容，不包装整条调用。", desktopAwcpJSONType(value, true)), desktopAwcpJSONType(value, true), nil)
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
		return nil, desktopActionErrorResult(codePrefix+"_read_failed", desktopInputReadError(field, err), nil), true
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
		return nil, desktopActionErrorResult(codePrefix+"_read_failed", desktopInputReadError(field, err), nil), true
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, desktopActionErrorResult(codePrefix+"_read_failed", desktopInputReadError(field, err), nil), true
	}
	if len(data) > maxBytes {
		return tooLarge()
	}
	return data, ToolExecutionResult{}, false
}

func desktopInputReadError(field string, err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return field + " 文件不存在；请创建参数文件或修正路径。"
	}
	if errors.Is(err, os.ErrPermission) {
		return field + " 文件不可读；请修正文件读取权限或使用有权读取的参数文件。"
	}
	return field + " 无法读取；请检查路径及文件读取权限。"
}

// SyntaxError.Offset is a one-based byte offset; report one-based Unicode columns.
func desktopParamsJSONPosition(data []byte, offset int64) (int, int) {
	end := int(offset - 1)
	if end < 0 {
		end = 0
	}
	if end > len(data) {
		end = len(data)
	}
	line, column := 1, 1
	for _, r := range string(data[:end]) {
		if r == '\n' {
			line++
			column = 1
		} else {
			column++
		}
	}
	return line, column
}
