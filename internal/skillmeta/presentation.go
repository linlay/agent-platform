// Package skillmeta defines locale-aware, display-only skill metadata.
package skillmeta

import (
	"sort"
	"strings"

	"golang.org/x/text/language"
)

// Presentation retains translations privately; only resolved fields are public.
type Presentation struct {
	DisplayName  string          `json:"displayName"`
	Version      string          `json:"version,omitempty"`
	Revision     string          `json:"revision,omitempty"`
	Translations map[string]Text `json:"-"`
}

type Text struct{ DisplayName, Description string }

func String(v any) string { s, _ := v.(string); return strings.TrimSpace(s) }

func Parse(metadata map[string]any, version string) Presentation {
	p := Presentation{DisplayName: String(metadata["displayName"]), Version: version, Revision: String(metadata["revision"])}
	translations, _ := metadata["i18n"].(map[string]any)
	keys := make([]string, 0, len(translations))
	for key := range translations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, locale := range keys {
		if _, err := language.Parse(locale); err != nil || strings.TrimSpace(locale) == "" {
			continue
		}
		value := translations[locale]
		fields, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if p.Translations == nil {
			p.Translations = map[string]Text{}
		}
		p.Translations[strings.ToLower(strings.TrimSpace(locale))] = Text{String(fields["displayName"]), String(fields["description"])}
	}
	return p
}

// Resolve returns a copy and never changes catalog metadata or model prompts.
func (p Presentation) Resolve(locale, name, key, description string) (Presentation, string) {
	locale = strings.ToLower(strings.TrimSpace(locale))
	exact := p.Translations[locale]
	base := p.Translations[strings.SplitN(locale, "-", 2)[0]]
	p.DisplayName = first(exact.DisplayName, base.DisplayName, p.DisplayName, name, key)
	description = first(exact.Description, base.Description, description)
	p.Translations = nil
	return p, description
}

func first(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
