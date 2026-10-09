package knowledge

import (
	"fmt"
	"strings"
)

// ValidateSourcePattern accepts only patterns with equivalent Platform/KBX
// separator semantics, before either extraction or embedding can begin.
func ValidateSourcePattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" || strings.HasPrefix(pattern, "/") || strings.ContainsAny(pattern, "[]{}\\\x00") {
		return fmt.Errorf("invalid knowledge source pattern %q", pattern)
	}
	parts := strings.Split(pattern, "/")
	for i, p := range parts {
		if p == ".." || p == "." || p == "" {
			return fmt.Errorf("invalid knowledge source pattern %q", pattern)
		}
		if p == "**" {
			continue
		}
		if strings.ContainsAny(p, "*?") && (i != len(parts)-1 || i == 0 || parts[i-1] != "**" || !strings.HasPrefix(p, "*") || strings.ContainsAny(p[1:], "*?")) {
			return fmt.Errorf("source glob %q has incompatible directory semantics; use explicit paths, dir/** or **/*.ext", pattern)
		}
	}
	return nil
}
func ValidateSourceChunk(c ChunkConfig) error {
	if c == (ChunkConfig{}) {
		return nil
	}
	if c.Unit == ChunkUnitChars && c.MaxChars > 0 && c.OverlapChars >= 0 && c.OverlapChars < c.MaxChars && c.MaxTokens == 0 && c.OverlapTokens == 0 {
		return nil
	}
	if c == DefaultChunkConfig() {
		return nil
	}
	return fmt.Errorf("custom collection chunking requires unit: chars, positive maxChars and 0 <= overlapChars < maxChars")
}
