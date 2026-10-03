package llm

import (
	"agent-platform/internal/api"
	"encoding/json"
	"strings"
	"testing"
)

func TestModelToolSpecsExcludePresentation(t *testing.T) {
	def := api.ToolDetailResponse{Name: "custom", Label: "展示名称", Description: "Original English instructions", Parameters: map[string]any{"type": "object"}, Meta: map[string]any{"toolI18n": map[string]any{"zh-CN": map[string]any{"label": "自定义", "description": "仅界面说明"}}}}
	data, err := json.Marshal(toOpenAIToolSpecs([]api.ToolDetailResponse{def}))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"label", "i18n", "I18n", "展示", "界面"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("presentation leaked: %s", text)
		}
	}
	if !strings.Contains(text, "Original English instructions") || !strings.Contains(text, "parameters") {
		t.Fatal(text)
	}
}
