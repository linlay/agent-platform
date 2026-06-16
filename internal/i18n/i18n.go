package i18n

import (
	"encoding/json"
	"strings"
)

const (
	DefaultLocale = "en"
	LocaleEN      = "en"
	LocaleZhCN    = "zh-CN"
)

var supportedLocales = []string{LocaleEN, LocaleZhCN}

var zhCNMessages = map[string]string{
	"accessLevel must be default, auto_approve, or full_access": "accessLevel 必须是 default、auto_approve 或 full_access",
	"agent not found":                                     "智能体不存在",
	"agentKey is required":                                "agentKey 不能为空",
	"archive not found":                                   "归档不存在",
	"chat not found":                                      "会话不存在",
	"chatId and chatName are required":                    "chatId 和 chatName 不能为空",
	"chatId and message are required":                     "chatId 和 message 不能为空",
	"chatId and runId are required":                       "chatId 和 runId 不能为空",
	"chatId is required":                                  "chatId 不能为空",
	"chatId or agentKey is required":                      "chatId 或 agentKey 不能为空",
	"file is required":                                    "file 不能为空",
	"frame must be request":                               "frame 必须是 request",
	"id is required":                                      "id 不能为空",
	"includeChats must be between 0 and 50":               "includeChats 必须在 0 到 50 之间",
	"includeChats must be greater than or equal to 0":     "includeChats 必须大于或等于 0",
	"includeChats must be less than or equal to 50":       "includeChats 必须小于或等于 50",
	"includeChats must be an integer":                     "includeChats 必须是整数",
	"invalid access-level payload":                        "access-level 请求内容无效",
	"invalid attach payload":                              "attach 请求内容无效",
	"invalid chatId":                                      "chatId 无效",
	"invalid detach payload":                              "detach 请求内容无效",
	"invalid json frame":                                  "JSON frame 无效",
	"invalid locale":                                      "语言设置无效",
	"invalid multipart form":                              "multipart form 无效",
	"invalid payload":                                     "请求内容无效",
	"invalid request body":                                "请求体无效",
	"invalid resource payload":                            "resource 请求内容无效",
	"invalid submit payload":                              "submit 请求内容无效",
	"message is required":                                 "message 不能为空",
	"method not allowed":                                  "请求方法不允许",
	"memory not found":                                    "记忆不存在",
	"memory system is disabled":                           "记忆系统已禁用",
	"multiple active runs found for chat":                 "当前会话存在多个活跃运行",
	"query is required":                                   "query 不能为空",
	"resource access denied":                              "资源访问被拒绝",
	"resource not found":                                  "资源不存在",
	"resource ticket chat mismatch":                       "资源票据与会话不匹配",
	"resource ticket required":                            "需要资源票据",
	"role must be user, assistant, automation, or system": "role 必须是 user、assistant、automation 或 system",
	"run event bus unavailable":                           "运行事件总线不可用",
	"run not found":                                       "运行不存在",
	"runId and message are required":                      "runId 和 message 不能为空",
	"runId is required":                                   "runId 不能为空",
	"tool not found":                                      "工具不存在",
	"tool result access denied":                           "工具结果访问被拒绝",
	"tool result not found":                               "工具结果不存在",
	"toolName is required":                                "toolName 不能为空",
	"too many active streams":                             "活跃流数量过多",
	"too many observers":                                  "观察者数量过多",
	"type is required":                                    "type 不能为空",
	"type must be thumbs_down or clear":                   "type 必须是 thumbs_down 或 clear",
	"unauthorized":                                        "未授权",
	"unknown type":                                        "未知类型",
	"viewportKey is required":                             "viewportKey 不能为空",
}

var zhCNCodes = map[string]string{
	"active_run_conflict":                    "当前会话存在活跃运行冲突",
	"agent_forbidden":                        "当前渠道不允许使用该智能体",
	"agent_not_found":                        "智能体不存在",
	"agent_registry_unavailable":             "智能体注册表不可用",
	"archive_not_found":                      "归档不存在",
	"archive_operation_failed":               "归档操作失败",
	"archive_unavailable":                    "归档服务不可用",
	"automation_execution_store_unavailable": "自动化执行记录不可用",
	"automation_id_allocate_failed":          "自动化 ID 分配失败",
	"automation_invalid":                     "自动化配置无效",
	"automation_not_found":                   "自动化不存在",
	"automation_unavailable":                 "自动化服务不可用",
	"budget_exceeded":                        "运行预算已用尽",
	"channel_forbidden":                      "当前渠道不允许该操作",
	"chat_not_found":                         "会话不存在",
	"chat_store_unavailable":                 "会话存储不可用",
	"configuration_error":                    "配置错误",
	"duplicate_id":                           "请求 id 已在处理中",
	"duplicate_observe":                      "当前连接已在观察该运行",
	"duplicate_stream":                       "请求已存在活跃流",
	"event_bus_unavailable":                  "运行事件总线不可用",
	"external_tool_call_failed":              "外部工具调用失败",
	"file_history_not_found":                 "文件历史不存在",
	"file_history_unavailable":               "文件历史不可用",
	"forbidden":                              "无权限访问",
	"frontend_submit_invalid_payload":        "前端提交内容无效",
	"frontend_submit_timeout":                "等待前端提交超时",
	"frontend_tool_handler_not_registered":   "前端工具处理器未注册",
	"hitl_rejected":                          "用户拒绝了该操作",
	"hitl_rejected_with_feedback":            "用户拒绝并给出了修改意见",
	"hitl_timeout":                           "等待用户审批超时",
	"invalid_field":                          "字段无效",
	"internal_error":                         "内部错误",
	"invalid_file_history_request":           "文件历史请求无效",
	"invalid_locale":                         "语言设置无效",
	"invalid_payload":                        "请求内容无效",
	"invalid_request":                        "请求无效",
	"invalid_upload_metadata":                "上传元数据无效",
	"mcp_call_failed":                        "MCP 调用失败",
	"memory_disabled":                        "记忆系统已禁用",
	"memory_history_unavailable":             "记忆历史不可用",
	"memory_not_found":                       "记忆不存在",
	"memory_operation_failed":                "记忆操作失败",
	"memory_store_unavailable":               "记忆存储不可用",
	"method_not_allowed":                     "请求方法不允许",
	"missing_required_field":                 "必填字段缺失",
	"model_calls_exceeded":                   "模型调用次数已达上限",
	"model_not_found":                        "模型不存在",
	"model_registry_unavailable":             "模型注册表不可用",
	"not_found":                              "未找到",
	"observer_attach_failed":                 "观察运行流失败",
	"plan_context_unavailable":               "计划上下文不可用",
	"planning_mode_unsupported":              "当前智能体不支持规划模式",
	"policy_denied":                          "安全策略拒绝了该操作",
	"provider_auth_failed":                   "模型服务鉴权失败",
	"provider_bad_request":                   "模型服务请求无效",
	"provider_bad_response":                  "模型服务返回异常",
	"provider_content_filter":                "模型服务拒绝了该内容",
	"provider_context_length_exceeded":       "上下文长度超过模型限制",
	"provider_model_not_found":               "模型服务中不存在该模型",
	"provider_network_error":                 "连接模型服务失败",
	"provider_permission_denied":             "模型服务权限不足",
	"provider_quota_exhausted":               "模型服务额度已用尽",
	"provider_rate_limited":                  "模型服务请求过于频繁",
	"provider_request_failed":                "模型服务请求失败",
	"provider_stream_failed":                 "模型服务流式响应失败",
	"provider_stream_invalid":                "模型服务流式响应无效",
	"provider_timeout":                       "模型服务请求超时",
	"provider_unavailable":                   "模型服务不可用",
	"proxy_bad_response":                     "代理服务返回异常",
	"proxy_config_missing":                   "代理配置缺失",
	"proxy_request_failed":                   "代理请求失败",
	"proxy_streaming_unsupported":            "代理服务不支持流式响应",
	"proxy_timeout":                          "代理请求超时",
	"proxy_upstream_error":                   "代理上游服务错误",
	"resource_forbidden":                     "资源访问被拒绝",
	"resource_not_found":                     "资源不存在",
	"resource_push_failed":                   "资源推送失败",
	"resource_read_failed":                   "资源读取失败",
	"resource_ticket_chat_mismatch":          "资源票据与会话不匹配",
	"resource_ticket_required":               "需要资源票据",
	"run_not_found":                          "运行不存在",
	"run_already_finished":                   "运行已结束",
	"run_cancelled":                          "运行已取消",
	"run_error":                              "运行失败",
	"run_interrupted":                        "运行已中断",
	"run_timeout":                            "运行超时",
	"seq_expired":                            "流序号已过期",
	"SEQ_EXPIRED":                            "流序号已过期",
	"service_unavailable":                    "服务不可用",
	"skill_candidate_store_unavailable":      "技能候选存储不可用",
	"storage_failed":                         "存储操作失败",
	"stream_failed":                          "运行流失败",
	"sub_agent_failed":                       "子智能体执行失败",
	"task_execution_error":                   "任务执行错误",
	"task_failed":                            "任务失败",
	"terminal_not_found":                     "终端不存在",
	"terminal_unavailable":                   "终端服务不可用",
	"terminal_unsupported":                   "终端不受支持",
	"timeout":                                "操作超时",
	"tool_args_invalid":                      "工具参数无效",
	"tool_calls_exceeded":                    "工具调用次数已达上限",
	"tool_failed":                            "工具调用失败",
	"too_many_observers":                     "观察者数量过多",
	"too_many_streams":                       "活跃流数量过多",
	"tool_not_found":                         "工具不存在",
	"tool_result_access_denied":              "工具结果访问被拒绝",
	"tool_result_forbidden":                  "工具结果访问被拒绝",
	"tool_result_not_found":                  "工具结果不存在",
	"tool_timeout":                           "工具调用超时",
	"unsupported":                            "不支持该操作",
	"unsupported_operation":                  "不支持该操作",
	"upload_failed":                          "上传失败",
	"unavailable":                            "服务不可用",
	"unauthorized":                           "未授权",
	"user_dismissed":                         "用户关闭等待项",
}

var zhCNPreferMessageCodes = map[string]bool{
	"active_run_conflict": true,
	"forbidden":           true,
	"internal_error":      true,
	"invalid_request":     true,
	"not_found":           true,
	"timeout":             true,
	"unavailable":         true,
	"unauthorized":        true,
}

// SupportedLocales returns the public locale identifiers accepted by this service.
func SupportedLocales() []string {
	return append([]string(nil), supportedLocales...)
}

func IsSupported(locale string) bool {
	_, ok := NormalizeLocale(locale)
	return ok
}

func NormalizeLocale(locale string) (string, bool) {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"))
	switch {
	case normalized == "":
		return "", false
	case normalized == "en" || strings.HasPrefix(normalized, "en-"):
		return LocaleEN, true
	case normalized == "zh" || normalized == "zh-cn" || normalized == "zh-hans" || strings.HasPrefix(normalized, "zh-hans-"):
		return LocaleZhCN, true
	case strings.HasPrefix(normalized, "zh-"):
		return LocaleZhCN, true
	default:
		return "", false
	}
}

func ResolveLocale(defaultLocale string, candidates ...string) string {
	for _, candidate := range candidates {
		if locale, ok := NormalizeLocale(candidate); ok {
			return locale
		}
	}
	if locale, ok := NormalizeLocale(defaultLocale); ok {
		return locale
	}
	return DefaultLocale
}

func LocaleFromHTTP(queryLocale string, headerLocale string, acceptLanguage string, defaultLocale string) string {
	if locale, ok := NormalizeLocale(queryLocale); ok {
		return locale
	}
	if locale, ok := NormalizeLocale(headerLocale); ok {
		return locale
	}
	if locale, ok := LocaleFromAcceptLanguage(acceptLanguage); ok {
		return locale
	}
	return ResolveLocale(defaultLocale)
}

func LocaleFromAcceptLanguage(header string) (string, bool) {
	for _, part := range strings.Split(header, ",") {
		tag := strings.TrimSpace(part)
		if tag == "" {
			continue
		}
		if idx := strings.IndexByte(tag, ';'); idx >= 0 {
			tag = strings.TrimSpace(tag[:idx])
		}
		if locale, ok := NormalizeLocale(tag); ok {
			return locale, true
		}
	}
	return "", false
}

func Translate(locale string, code string, message string) string {
	locale = ResolveLocale(locale)
	message = strings.TrimSpace(message)
	code = strings.TrimSpace(code)
	if locale != LocaleZhCN {
		return message
	}
	if zhCNPreferMessageCodes[code] {
		if translated := zhCNMessages[message]; translated != "" {
			return translated
		}
		if message == "" || message == code {
			if translated := zhCNCodes[code]; translated != "" {
				return translated
			}
		}
		return message
	}
	if translated := zhCNCodes[code]; translated != "" {
		return translated
	}
	if translated := zhCNMessages[message]; translated != "" {
		return translated
	}
	return message
}

func LocalizeValue(locale string, value any) any {
	if ResolveLocale(locale) == LocaleEN || value == nil {
		return value
	}
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return value
	}
	return localizeJSONValue(locale, decoded)
}

func LocalizeEventPayload(locale string, eventType string, payload map[string]any) map[string]any {
	if ResolveLocale(locale) == LocaleEN || payload == nil {
		return payload
	}
	out, _ := localizeJSONValue(locale, payload).(map[string]any)
	if len(out) == 0 {
		return payload
	}
	switch eventType {
	case "run.error", "task.error", "awaiting.answer":
		return out
	default:
		return cloneMap(payload)
	}
}

func localizeJSONValue(locale string, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = localizeJSONValue(locale, child)
		}
		code, _ := out["code"].(string)
		if message, ok := out["message"].(string); ok {
			out["message"] = Translate(locale, code, message)
		}
		msgCode := code
		if msgCode == "" {
			msgCode, _ = out["type"].(string)
		}
		if msg, ok := out["msg"].(string); ok {
			out["msg"] = Translate(locale, msgCode, msg)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			out = append(out, localizeJSONValue(locale, child))
		}
		return out
	default:
		return value
	}
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
