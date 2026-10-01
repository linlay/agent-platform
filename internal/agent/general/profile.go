// Package general holds the rules that belong only to the general-purpose
// native agent type. It has no fixed tool set, prompt or stage of its own;
// those come from each agent.yml.
package general

import "strings"

const (
	Mode = "GENERAL"
	// legacyMode is the spelling used before the type was renamed. It stays
	// accepted so existing agent files and chat records keep their meaning.
	legacyMode = "REACT"
)

func IsMode(mode string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(mode))
	return normalized == Mode || normalized == legacyMode
}
