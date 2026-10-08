package builtins

import (
	"fmt"
	"strconv"
	"strings"
)

const MinimumMemxVersion = "0.2.0"

// RequireMemxVersion is shared by release validation and runtime probes. The
// current memory policy assumes plain daily Markdown with private provenance.
func RequireMemxVersion(version string) error {
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	if len(parts) == 3 {
		major, a := strconv.Atoi(parts[0])
		minor, b := strconv.Atoi(parts[1])
		patch, c := strconv.Atoi(parts[2])
		if a == nil && b == nil && c == nil && major >= 0 && minor >= 0 && patch >= 0 && (major > 0 || minor > 2 || minor == 2 && patch >= 0) {
			return nil
		}
	}
	return fmt.Errorf("memx >= %s with plain Markdown and private provenance required (found %q); synchronize builtins", MinimumMemxVersion, version)
}

// RequireMemxComponent rejects release caches predating memx distribution.
// VerifyManifest checks the actual executable and archive-derived license bytes.
func RequireMemxComponent(manifest Manifest) error {
	expected := "bin/memx"
	if manifest.Platform.OS == "windows" {
		expected += ".exe"
	}
	for _, component := range manifest.Components {
		if component.Name == "memx" && component.Path == expected && len(component.Tree) == 0 {
			return RequireMemxVersion(component.Version)
		}
	}
	return fmt.Errorf("release requires memx >= %s at %s; run scripts/sync-local-builtins to rebuild the cache", MinimumMemxVersion, expected)
}
