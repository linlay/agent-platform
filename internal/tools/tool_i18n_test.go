package tools

import (
	"agent-platform/internal/i18n"
	"testing"
	"unicode"
)

func TestEmbeddedToolPresentationContract(t *testing.T) {
	defs, err := LoadEmbeddedToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range defs {
		t.Run(def.Name, func(t *testing.T) {
			translations, ok := def.Meta["toolI18n"].(map[string]any)
			if !ok {
				t.Fatal("missing translations")
			}
			for _, locale := range []string{"en", "zh-CN"} {
				fields, ok := translations[locale].(map[string]any)
				if !ok || fields["label"] == nil {
					t.Fatalf("missing %s label", locale)
				}
				if _, exists := fields["description"]; exists {
					t.Fatal("builtin description must not be translated")
				}
			}
			var check func(any)
			check = func(value any) {
				switch v := value.(type) {
				case map[string]any:
					for key, child := range v {
						if key == "description" {
							if text, ok := child.(string); ok {
								for _, r := range text {
									if unicode.Is(unicode.Han, r) {
										t.Errorf("non-English description: %s", text)
										break
									}
								}
							}
						}
						check(child)
					}
				case []any:
					for _, child := range v {
						check(child)
					}
				}
			}
			check(map[string]any{"description": def.Description, "schema": def.Parameters, "output": def.OutputSchema})
		})
	}
}

func TestToolLoaderAcceptsPresentationDescriptionWithoutChangingModel(t *testing.T) {
	def, err := parseToolDefinition(map[string]any{"name": "custom", "description": "Model instructions", "i18n": map[string]any{"zh-CN": map[string]any{"label": "自定义", "description": "界面说明"}}}, toolDefinitionParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := i18n.LocalizeValue("zh-CN", def).(map[string]any)
	if got["description"] != "界面说明" || def.Description != "Model instructions" {
		t.Fatalf("description isolation: %#v", got)
	}
}
