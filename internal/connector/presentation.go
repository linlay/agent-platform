package connector

import (
	"strings"

	"agent-platform/internal/i18n"
)

type Translation struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// Localized resolves response presentation without modifying the package or its digest.
func (m Manifest) Localized(locale string) Manifest {
	if value, ok := m.I18N[i18n.ResolveLocale(locale)]; ok {
		if strings.TrimSpace(value.Name) != "" {
			m.Name = value.Name
		}
		if strings.TrimSpace(value.Description) != "" {
			m.Description = value.Description
		}
	}
	m.I18N = nil
	return m
}
