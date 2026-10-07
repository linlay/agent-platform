package i18n

import (
	"reflect"
	"strings"
	"testing"
)

func TestReasoningLabelPool(t *testing.T) {
	if len(reasoningLabels) != 30 {
		t.Fatalf("expected 30 bilingual phrases, got %d", len(reasoningLabels))
	}
	seenEN, seenZH := map[string]bool{}, map[string]bool{}
	for _, label := range reasoningLabels {
		if label.en == "" || label.zhCN == "" || seenEN[label.en] || seenZH[label.zhCN] {
			t.Fatalf("empty or duplicate phrase: %+v", label)
		}
		if !strings.HasSuffix(label.en, "ing") || strings.ContainsAny(label.en, " \t\n") {
			t.Fatalf("expected a single English -ing word: %q", label.en)
		}
		if strings.Contains(label.zhCN, "中") || strings.Contains(label.zhCN, "正在") {
			t.Fatalf("Chinese phrase contains a prohibited word: %q", label.zhCN)
		}
		seenEN[label.en], seenZH[label.zhCN] = true, true
	}
}

func TestReasoningLabelFNV1a32Vectors(t *testing.T) {
	// Fixed vectors distinguish FNV-1a from FNV-1, 64-bit hashing, rune
	// hashing, and a changed pool size/order.
	for _, tc := range []struct{ id, en, zh string }{
		{"", "Thinking", "思考"},
		{" \t\n", "Thinking", "思考"},
		{"hello", "Cogitating", "思索"},        // 0x4f9f2cab % 30 = 3
		{"run_1_r_1", "Simmering", "慢炖思绪"},   // 0x6de5204b % 30 = 25
		{"run_1_r_2", "Percolating", "萃取灵感"}, // 0x6ee521de % 30 = 24
		{"思考_1", "Brewing", "酝酿"},            // 0x39061937 % 30 = 23
		{" \thello\n", "Cogitating", "思索"},
	} {
		for _, locale := range []string{"en", "en-US", "", "unsupported"} {
			if got := ReasoningLabelForID(locale, tc.id); got != tc.en {
				t.Fatalf("locale=%q id=%q: got %q, want %q", locale, tc.id, got, tc.en)
			}
		}
		for _, locale := range []string{"zh-CN", "zh-cn", "zh_CN"} {
			if got := ReasoningLabelForID(locale, tc.id); got != tc.zh {
				t.Fatalf("locale=%q id=%q: got %q, want %q", locale, tc.id, got, tc.zh)
			}
		}
	}
}

func TestReasoningPresentationLocalizesPerViewer(t *testing.T) {
	for _, eventType := range []string{"reasoning.start", "reasoning.snapshot"} {
		for _, legacyLabel := range []string{"正在思考", "reasoning_details", ""} {
			original := map[string]any{
				"reasoningId": "hello", "runId": "run", "taskId": "task",
				"reasoningLabel": legacyLabel, "text": "原始推理内容",
			}
			for _, tc := range []struct{ locale, label string }{{"zh-cn", "思索"}, {"en", "Cogitating"}} {
				got := LocalizeEventPayload(tc.locale, eventType, original)
				if got["reasoningLabel"] != tc.label || got["text"] != original["text"] || got["reasoningId"] != "hello" || got["taskId"] != "task" {
					t.Fatalf("%s %s: %#v", eventType, tc.locale, got)
				}
				if len(got) != len(original) {
					t.Fatalf("localization changed protocol fields: %#v", got)
				}
				got["text"] = "changed"
			}
			if original["reasoningLabel"] != legacyLabel || original["text"] != "原始推理内容" {
				t.Fatalf("source event was mutated: %#v", original)
			}
		}
		for _, tc := range []struct{ locale, label string }{{"en", "Thinking"}, {"zh-cn", "思考"}} {
			if got := LocalizeEventPayload(tc.locale, eventType, map[string]any{"reasoningId": ""}); got["reasoningLabel"] != tc.label {
				t.Fatalf("empty ID/missing label fallback: %#v", got)
			}
		}
	}
	for _, eventType := range []string{"reasoning.delta", "reasoning.end", "content.snapshot"} {
		payload := map[string]any{"reasoningId": "hello", "delta": "original"}
		if got := LocalizeEventPayload("zh-cn", eventType, payload); !reflect.DeepEqual(got, payload) {
			t.Fatalf("unrelated event was changed: %s %#v", eventType, got)
		}
	}
}

func TestReasoningHistoryLocalizesLegacyAndMissingLabels(t *testing.T) {
	value := map[string]any{"events": []any{
		map[string]any{"type": "reasoning.snapshot", "reasoningId": "hello", "reasoningLabel": "正在思考", "text": "原始推理内容"},
		map[string]any{"type": "reasoning.start", "reasoningId": ""},
		map[string]any{"type": "reasoning.delta", "reasoningId": "hello", "delta": "原始推理内容"},
	}}
	for _, tc := range []struct{ locale, label, fallback string }{{"zh-cn", "思索", "思考"}, {"en", "Cogitating", "Thinking"}} {
		got := LocalizeValue(tc.locale, value).(map[string]any)["events"].([]any)
		if got[0].(map[string]any)["reasoningLabel"] != tc.label || got[0].(map[string]any)["text"] != "原始推理内容" || got[1].(map[string]any)["reasoningLabel"] != tc.fallback {
			t.Fatalf("%s history: %#v", tc.locale, got)
		}
		if _, added := got[2].(map[string]any)["reasoningLabel"]; added {
			t.Fatalf("label added to delta: %#v", got[2])
		}
	}
	if value["events"].([]any)[0].(map[string]any)["reasoningLabel"] != "正在思考" {
		t.Fatal("history source was mutated")
	}
}
