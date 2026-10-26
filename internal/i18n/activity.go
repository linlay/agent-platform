package i18n

import (
	"fmt"
	"strings"
)

// run.activity carries a message template in the internal "activity" field;
// the output boundary renders it in the viewer's locale and removes the
// template. A retry activity carries "retry" instead and renders from it.

type activityText struct{ en, zhCN string }

var activityTexts = map[string]activityText{
	"thinking":   {"Thinking…", "正在思考"},
	"compacting": {"Organizing context…", "正在整理上下文"},
}

var awaitingTexts = map[string]activityText{
	"approval": {"Waiting for your approval", "等待你审批"},
	"question": {"Waiting for your answer", "等待你回答"},
	"form":     {"Waiting for you to fill in the form", "等待你填写表单"},
	"planning": {"Waiting for you to confirm the plan", "等待你确认计划"},
}

var awaitingFallback = activityText{"Waiting for your response", "等待你回应"}

func (t activityText) in(locale string) string {
	if ResolveLocale(locale) == LocaleZhCN {
		return t.zhCN
	}
	return t.en
}

// ActivityMessage renders one activity template. "idle" (the user is reading
// visible output) and unknown kinds render empty.
func ActivityMessage(locale string, activity map[string]any) string {
	zh := ResolveLocale(locale) == LocaleZhCN
	kind, _ := activity["kind"].(string)
	switch kind {
	case "reasoning":
		reasoningID, _ := activity["reasoningId"].(string)
		label := ReasoningLabelForID(locale, reasoningID)
		if zh {
			return "正在" + label
		}
		return label + "…"
	case "announce":
		// The model wrote it in the user's language; it is shown as is.
		text, _ := activity["text"].(string)
		return text
	case "tool_run":
		label := activityToolLabel(locale, activity)
		if text, _ := activity["text"].(string); text != "" {
			if zh {
				return label + "：" + text
			}
			return label + ": " + text
		}
		if zh {
			return "正在调用「" + label + "」"
		}
		return fmt.Sprintf("Calling %q…", label)
	case "subtask":
		name, _ := activity["taskName"].(string)
		switch {
		case zh && name != "":
			return "正在委派子任务「" + name + "」"
		case zh:
			return "正在委派子任务"
		case name != "":
			return fmt.Sprintf("Delegating %q…", name)
		default:
			return "Delegating a subtask…"
		}
	case "awaiting":
		mode, _ := activity["mode"].(string)
		if text, ok := awaitingTexts[mode]; ok {
			return text.in(locale)
		}
		return awaitingFallback.in(locale)
	}
	if text, ok := activityTexts[kind]; ok {
		return text.in(locale)
	}
	return ""
}

// activityToolLabel resolves the tool label the same way tool events do:
// the viewer's translation, then the frozen label, then the tool name.
func activityToolLabel(locale string, activity map[string]any) string {
	if translations, ok := activity["toolI18n"].(map[string]any); ok {
		if translation, ok := translations[ResolveLocale(locale)].(map[string]any); ok {
			if label, _ := translation["label"].(string); strings.TrimSpace(label) != "" {
				return label
			}
		}
	}
	if label, _ := activity["toolLabel"].(string); strings.TrimSpace(label) != "" {
		return label
	}
	name, _ := activity["toolName"].(string)
	return name
}

// RetryActivityMessage renders the model retry notice from its retry object.
// The shown ordinal excludes the first request.
func RetryActivityMessage(locale string, retry map[string]any) string {
	zh := ResolveLocale(locale) == LocaleZhCN
	reason, _ := retry["reason"].(string)
	timeout := reason == "provider_timeout" || reason == "model_stream_idle_timeout"
	prefix := "Model request failed"
	if zh {
		prefix = "模型请求失败"
	}
	if timeout {
		prefix = "Model response timed out"
		if zh {
			prefix = "模型响应超时"
		}
	}
	delayMs := activityNumber(retry["delayMs"])
	if delayMs <= 0 {
		if zh {
			return prefix + "，正在重试"
		}
		return prefix + "; retrying"
	}
	attempt := int(activityNumber(retry["attempt"])) - 1
	total := int(activityNumber(retry["maxAttempts"])) - 1
	seconds := delayMs / 1000
	if zh {
		return fmt.Sprintf("%s，%g 秒后第 %d/%d 次重试", prefix, seconds, attempt, total)
	}
	return fmt.Sprintf("%s; retry %d/%d in %gs", prefix, attempt, total, seconds)
}

func activityNumber(value any) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case float64:
		return typed
	}
	return 0
}

// localizeActivityPayload renders message for the viewer and drops the
// internal template.
func localizeActivityPayload(locale string, payload map[string]any) map[string]any {
	out := cloneMap(payload)
	if activity, ok := out["activity"].(map[string]any); ok {
		out["message"] = ActivityMessage(locale, activity)
		delete(out, "activity")
	} else if retry, ok := out["retry"].(map[string]any); ok {
		out["message"] = RetryActivityMessage(locale, retry)
	}
	return out
}
