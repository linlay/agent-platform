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
	"active_run_conflict": "当前会话存在活跃运行冲突",
	"duplicate_id":        "请求 id 已在处理中",
	"duplicate_observe":   "当前连接已在观察该运行",
	"forbidden":           "无权限访问",
	"internal_error":      "内部错误",
	"invalid_locale":      "语言设置无效",
	"invalid_request":     "请求无效",
	"not_found":           "未找到",
	"resource_forbidden":  "资源访问被拒绝",
	"resource_not_found":  "资源不存在",
	"run_not_found":       "运行不存在",
	"task_failed":         "任务失败",
	"timeout":             "操作超时",
	"too_many_observers":  "观察者数量过多",
	"too_many_streams":    "活跃流数量过多",
	"unavailable":         "服务不可用",
	"unauthorized":        "未授权",
	"user_dismissed":      "用户关闭等待项",
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
