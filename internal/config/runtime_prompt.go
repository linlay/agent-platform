package config

import (
	"fmt"
	"regexp"
	"strings"

	"agent-platform/internal/i18n"
)

// DefaultEnvironmentPromptTemplate is a data header (labels and values only),
// not instructions, so it stays as the fallback.
const DefaultEnvironmentPromptTemplate = "Runtime Context: System Environment\nos: {{os}}\narch: {{arch}}\ntimezone: {{timezone}}\nlanguage: {{locale}}"

type RuntimePromptConfig struct {
	DefaultLocale             string
	EnvironmentPromptTemplate string
}

func (c RuntimePromptConfig) ResolveLocale(locale string) string {
	return i18n.ResolveLocale("zh-CN", locale, c.DefaultLocale)
}

func (c RuntimePromptConfig) Template() string {
	if strings.TrimSpace(c.EnvironmentPromptTemplate) == "" {
		return DefaultEnvironmentPromptTemplate
	}
	return c.EnvironmentPromptTemplate
}

var runtimePromptPlaceholder = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

// Render supports only the four environment header values, with one substitution pass.
func (c RuntimePromptConfig) Render(os, arch, timezone, locale string) string {
	values := map[string]string{"os": os, "arch": arch, "timezone": timezone, "locale": c.ResolveLocale(locale)}
	return strings.TrimSpace(runtimePromptPlaceholder.ReplaceAllStringFunc(c.Template(), func(token string) string {
		return values[runtimePromptPlaceholder.FindStringSubmatch(token)[1]]
	}))
}

func (c *Config) applyRuntimePrompt(shared map[string]any, path string) error {
	m, err := optionalConfigMap(shared, "runtime", path, "default-locale", "environment-prompt-template")
	if err != nil {
		return err
	}
	for key, raw := range m {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s.runtime.%s must be a non-empty string", path, key)
		}
		switch key {
		case "default-locale":
			locale, ok := i18n.NormalizeLocale(value)
			if !ok {
				return fmt.Errorf("%s.runtime.default-locale must be en or zh-CN", path)
			}
			c.Prompts.Runtime.DefaultLocale = locale
		case "environment-prompt-template":
			for _, match := range runtimePromptPlaceholder.FindAllStringSubmatch(value, -1) {
				switch match[1] {
				case "os", "arch", "timezone", "locale":
				default:
					return fmt.Errorf("unknown runtime prompt placeholder %q", match[1])
				}
			}
			remaining := runtimePromptPlaceholder.ReplaceAllString(value, "")
			if strings.ContainsAny(remaining, "{}") || strings.Contains(remaining, "${") {
				return fmt.Errorf("invalid runtime prompt placeholder; use {{os}}, {{arch}}, {{timezone}}, {{locale}}")
			}
			c.Prompts.Runtime.EnvironmentPromptTemplate = value
		}
	}
	return nil
}
