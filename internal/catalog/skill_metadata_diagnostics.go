package catalog

import (
	"fmt"
	"sort"
	"strings"

	"agent-platform/internal/skillmeta"
	"golang.org/x/text/language"
)

func skillMetadataDiagnostics(key, prompt string) []SkillCandidateDiagnostic {
	front, _ := parseSkillFrontMatter(prompt)
	metadata := frontMatterMap(front["metadata"])
	var out []SkillCandidateDiagnostic
	warn := func(code, message string) {
		out = append(out, SkillCandidateDiagnostic{Severity: "warning", Code: code, Message: message})
	}
	if name := skillmeta.String(front["name"]); name != "" && name != strings.TrimSpace(key) {
		warn("skill_name_key_mismatch", fmt.Sprintf("SKILL.md name %q differs from skill key %q; the directory key remains the identifier", name, key))
	}
	if value, exists := front["displayName"]; exists {
		if _, valid := value.(string); !valid {
			warn("invalid_skill_metadata", "displayName must be a string")
		}
	}
	if top, nested := skillmeta.String(front["displayName"]), skillmeta.String(metadata["displayName"]); top != "" && nested != "" && top != nested {
		warn("skill_display_name_conflict", "displayName differs from metadata.displayName; top-level displayName takes precedence")
	}
	top, nested := skillmeta.String(front["version"]), skillmeta.String(metadata["version"])
	if top != "" && nested != "" && top != nested {
		warn("skill_version_conflict", "version differs from metadata.version; top-level version takes precedence")
	}
	for _, key := range []string{"displayName", "version", "revision"} {
		if value, ok := metadata[key]; ok {
			if _, valid := value.(string); !valid {
				warn("invalid_skill_metadata", "metadata."+key+" must be a string")
			}
		}
	}
	raw, exists := metadata["i18n"]
	if !exists {
		return out
	}
	translations, ok := raw.(map[string]any)
	if !ok {
		warn("invalid_skill_i18n", "metadata.i18n must be an object")
		return out
	}
	keys := make([]string, 0, len(translations))
	for key := range translations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, locale := range keys {
		normalized := strings.ToLower(strings.TrimSpace(locale))
		if _, err := language.Parse(locale); err != nil || normalized == "" {
			warn("invalid_skill_locale", fmt.Sprintf("invalid skill locale %q", locale))
			continue
		}
		if seen[normalized] {
			warn("duplicate_skill_locale", fmt.Sprintf("duplicate skill locale %q", locale))
		}
		seen[normalized] = true
		fields, ok := translations[locale].(map[string]any)
		if !ok {
			warn("invalid_skill_i18n", "metadata.i18n."+locale+" must be an object")
			continue
		}
		for _, key := range []string{"displayName", "description"} {
			if value, exists := fields[key]; exists {
				if _, ok := value.(string); !ok {
					warn("invalid_skill_i18n", "metadata.i18n."+locale+"."+key+" must be a string")
				}
			}
		}
	}
	return out
}
