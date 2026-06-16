package i18n

import "testing"

func TestNormalizeLocale(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "en", want: LocaleEN, ok: true},
		{in: "en-US", want: LocaleEN, ok: true},
		{in: "zh", want: LocaleZhCN, ok: true},
		{in: "zh_CN", want: LocaleZhCN, ok: true},
		{in: "zh-Hans", want: LocaleZhCN, ok: true},
		{in: "fr", ok: false},
	}
	for _, tt := range tests {
		got, ok := NormalizeLocale(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("NormalizeLocale(%q) = %q, %t; want %q, %t", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestLocaleFromHTTP(t *testing.T) {
	if got := LocaleFromHTTP("", "", "fr, zh-CN;q=0.9, en;q=0.8", LocaleEN); got != LocaleZhCN {
		t.Fatalf("expected Accept-Language zh-CN, got %q", got)
	}
	if got := LocaleFromHTTP("en", "zh-CN", "", LocaleZhCN); got != LocaleEN {
		t.Fatalf("query locale should win, got %q", got)
	}
	if got := LocaleFromHTTP("", "", "", "bad"); got != LocaleEN {
		t.Fatalf("invalid default should fallback to en, got %q", got)
	}
}

func TestTranslateAndFallback(t *testing.T) {
	if got := Translate(LocaleZhCN, "not_found", "agent not found"); got != "智能体不存在" {
		t.Fatalf("expected translated message, got %q", got)
	}
	if got := Translate(LocaleZhCN, "not_found", "not_found"); got != "未找到" {
		t.Fatalf("expected code fallback, got %q", got)
	}
	if got := Translate(LocaleZhCN, "internal_error", "database exploded"); got != "database exploded" {
		t.Fatalf("dynamic messages should remain unchanged, got %q", got)
	}
	if got := Translate(LocaleZhCN, "provider_quota_exhausted", "model request failed with status 429: api key quota exhausted"); got != "模型服务额度已用尽" {
		t.Fatalf("provider code should translate independently of dynamic message, got %q", got)
	}
	if got := Translate(LocaleEN, "not_found", "agent not found"); got != "agent not found" {
		t.Fatalf("en should not translate, got %q", got)
	}
}

func TestLocalizeValue(t *testing.T) {
	value := map[string]any{
		"code": "not_found",
		"msg":  "agent not found",
		"data": map[string]any{
			"code":    "active_run_conflict",
			"message": "multiple active runs found for chat",
		},
	}
	localized, ok := LocalizeValue(LocaleZhCN, value).(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %#v", localized)
	}
	if localized["msg"] != "智能体不存在" {
		t.Fatalf("unexpected top-level msg %#v", localized["msg"])
	}
	data, _ := localized["data"].(map[string]any)
	if data["message"] != "当前会话存在多个活跃运行" {
		t.Fatalf("unexpected nested message %#v", data["message"])
	}
}

func TestLocalizeEventPayloadKeepsOriginal(t *testing.T) {
	payload := map[string]any{
		"error": map[string]any{
			"code":    "user_dismissed",
			"message": "用户关闭等待项",
		},
	}
	localized := LocalizeEventPayload(LocaleZhCN, "awaiting.answer", payload)
	errPayload := localized["error"].(map[string]any)
	if errPayload["message"] != "用户关闭等待项" {
		t.Fatalf("unexpected localized awaiting error %#v", errPayload)
	}
	errPayload["message"] = "changed"
	original := payload["error"].(map[string]any)
	if original["message"] != "用户关闭等待项" {
		t.Fatalf("expected original payload to remain untouched, got %#v", original)
	}
}
