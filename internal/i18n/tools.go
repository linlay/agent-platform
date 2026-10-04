package i18n

import (
	"fmt"
	"strings"
)

// ParseToolTranslations validates presentation-only metadata. It never changes
// model descriptions, parameter schemas, or callable tool names.
func ParseToolTranslations(raw any) (map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool i18n must be an object")
	}
	out := make(map[string]any, len(values))
	for key, rawTranslation := range values {
		locale, ok := NormalizeLocale(key)
		if !ok {
			return nil, fmt.Errorf("unsupported tool i18n locale %q", key)
		}
		if _, exists := out[locale]; exists {
			return nil, fmt.Errorf("duplicate tool i18n locale %q", locale)
		}
		translation, ok := rawTranslation.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tool i18n.%s must be an object", key)
		}
		fields := map[string]any{}
		for field, value := range translation {
			if field != "label" && field != "description" {
				return nil, fmt.Errorf("unknown tool i18n field %q", field)
			}
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("tool i18n.%s.%s must be a string", key, field)
			}
			if text = strings.TrimSpace(text); text != "" {
				fields[field] = text
			}
		}
		out[locale] = fields
	}
	return out, nil
}

// LocalizeToolPresentation only rewrites explicit tool presentation envelopes.
// Translations are removed from public output, and the source is never mutated.
func localizeToolPresentation(locale string, out map[string]any) {
	labelKey, descriptionKey := "label", "description"
	translations, _ := out["toolI18n"].(map[string]any)
	if _, isToolEvent := out["toolId"]; isToolEvent {
		labelKey, descriptionKey = "toolLabel", "toolDescription"
		delete(out, "toolI18n")
	} else if _, isTool := out["name"]; isTool {
		if meta, ok := out["meta"].(map[string]any); ok {
			translations, _ = meta["toolI18n"].(map[string]any)
			delete(meta, "toolI18n")
		}
		delete(out, "toolI18n")
	} else {
		return
	}
	if len(translations) == 0 {
		return
	}
	// A prior display label may belong to another viewer. Never use it as
	// the fallback once a translation table is available.
	name, _ := out["name"].(string)
	if labelKey == "toolLabel" {
		name, _ = out["toolName"].(string)
	}
	out[labelKey] = name
	if translation, ok := translations[ResolveLocale(locale)].(map[string]any); ok {
		for field, target := range map[string]string{"label": labelKey, "description": descriptionKey} {
			if value, ok := translation[field].(string); ok && strings.TrimSpace(value) != "" {
				out[target] = value
			}
		}
	}
	if label, _ := out[labelKey].(string); strings.TrimSpace(label) == "" {
		name, _ := out["name"].(string)
		if labelKey == "toolLabel" {
			name, _ = out["toolName"].(string)
		}
		out[labelKey] = name
	}
}

// CloneToolTranslations freezes the two-level, string-only translation table.
func CloneToolTranslations(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for locale, raw := range values {
		if fields, ok := raw.(map[string]any); ok {
			copy := make(map[string]any, len(fields))
			for key, value := range fields {
				copy[key] = value
			}
			out[locale] = copy
		}
	}
	return out
}
