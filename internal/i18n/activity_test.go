package i18n

import "testing"

func TestActivityMessageRendersViewerLocale(t *testing.T) {
	tool := map[string]any{
		"kind": "tool_run", "toolName": "file_write", "toolLabel": "Write File", "text": "README.md",
		"toolI18n": map[string]any{"zh-CN": map[string]any{"label": "写入文件"}},
	}
	for _, tc := range []struct {
		locale   string
		activity map[string]any
		want     string
	}{
		{LocaleZhCN, map[string]any{"kind": "thinking"}, "正在思考"},
		{LocaleEN, map[string]any{"kind": "thinking"}, "Thinking…"},
		{LocaleZhCN, map[string]any{"kind": "reasoning", "reasoningId": ""}, "正在思考"},
		{LocaleEN, map[string]any{"kind": "reasoning", "reasoningId": ""}, "Thinking…"},
		{LocaleZhCN, map[string]any{"kind": "responding"}, "正在回复"},
		{LocaleZhCN, tool, "写入文件：README.md"},
		{LocaleEN, tool, "Write File: README.md"},
		{LocaleZhCN, map[string]any{"kind": "tool_run", "toolName": "mcp_x"}, "正在调用「mcp_x」"},
		{LocaleZhCN, map[string]any{"kind": "tool_prepare", "toolLabel": "网页抓取"}, "正在准备「网页抓取」"},
		{LocaleZhCN, map[string]any{"kind": "subtask", "taskName": "竞品调研"}, "正在委派子任务「竞品调研」"},
		{LocaleZhCN, map[string]any{"kind": "awaiting", "mode": "approval"}, "等待你审批"},
		{LocaleZhCN, map[string]any{"kind": "awaiting", "mode": "question"}, "等待你回答"},
		{LocaleZhCN, map[string]any{"kind": "awaiting", "mode": "form"}, "等待你填写表单"},
		{LocaleZhCN, map[string]any{"kind": "awaiting", "mode": "planning"}, "等待你确认计划"},
		{LocaleZhCN, map[string]any{"kind": "compacting"}, "正在整理上下文"},
		{LocaleZhCN, map[string]any{"kind": "unknown"}, ""},
	} {
		if got := ActivityMessage(tc.locale, tc.activity); got != tc.want {
			t.Fatalf("%s %#v = %q want %q", tc.locale, tc.activity, got, tc.want)
		}
	}
}

func TestReasoningActivityUsesReasoningLabel(t *testing.T) {
	id := "run_1_r_7"
	activity := map[string]any{"kind": "reasoning", "reasoningId": id}
	if got, want := ActivityMessage(LocaleZhCN, activity), "正在"+ReasoningLabelForID(LocaleZhCN, id); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := ActivityMessage(LocaleEN, activity), ReasoningLabelForID(LocaleEN, id)+"…"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRetryActivityMessage(t *testing.T) {
	for _, tc := range []struct {
		locale string
		retry  map[string]any
		want   string
	}{
		{LocaleZhCN, map[string]any{"reason": "model_stream_idle_timeout", "attempt": 2, "maxAttempts": 6, "delayMs": int64(500)}, "模型响应超时，0.5 秒后第 1/5 次重试"},
		{LocaleEN, map[string]any{"reason": "provider_timeout", "attempt": 3, "maxAttempts": 6, "delayMs": int64(2000)}, "Model response timed out; retry 2/5 in 2s"},
		{LocaleZhCN, map[string]any{"reason": "provider_stream_failed"}, "模型请求失败，正在重试"},
	} {
		if got := RetryActivityMessage(tc.locale, tc.retry); got != tc.want {
			t.Fatalf("%#v = %q want %q", tc.retry, got, tc.want)
		}
	}
}

func TestLocalizeActivityPayloadDropsTemplate(t *testing.T) {
	source := map[string]any{"runId": "r", "message": "Thinking…", "activity": map[string]any{"kind": "thinking"}}
	out := LocalizeEventPayload(LocaleZhCN, "run.activity", source)
	if out["message"] != "正在思考" || out["activity"] != nil {
		t.Fatalf("unexpected localized activity %#v", out)
	}
	if source["activity"] == nil || source["message"] != "Thinking…" {
		t.Fatal("source payload mutated")
	}
	retry := LocalizeEventPayload(LocaleZhCN, "run.activity", map[string]any{"retry": map[string]any{"reason": "x"}})
	if retry["message"] != "模型请求失败，正在重试" || retry["retry"] == nil {
		t.Fatalf("unexpected localized retry %#v", retry)
	}
}
