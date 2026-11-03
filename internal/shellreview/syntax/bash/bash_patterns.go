package bash

import (
	"agent-platform/internal/shellreview/registry"
	"strings"
)

// StrictUnsupportedCommand uses the same metadata as syntax hard blocks;
// it preserves strict mode's exact-name check without basename expansion.
func StrictUnsupportedCommand(name string) bool {
	return registry.Is(strings.ToLower(name), registry.StrictUnsupported)
}
