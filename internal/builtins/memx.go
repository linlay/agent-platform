package builtins

import (
	"fmt"
	"strconv"
	"strings"
)

// RequireMemxComponent rejects release caches predating memx distribution.
// VerifyManifest checks the actual executable and archive-derived license bytes.
func RequireMemxComponent(manifest Manifest) error {
	expected := "bin/memx"
	if manifest.Platform.OS == "windows" {
		expected += ".exe"
	}
	for _, component := range manifest.Components {
		if component.Name == "memx" && component.Path == expected && len(component.Tree) == 0 {
			parts := strings.Split(strings.TrimPrefix(component.Version, "v"), ".")
			if len(parts) == 3 {
				major, a := strconv.Atoi(parts[0])
				minor, b := strconv.Atoi(parts[1])
				patch, c := strconv.Atoi(parts[2])
				if a == nil && b == nil && c == nil && major >= 0 && minor >= 0 && patch >= 0 && (major > 0 || minor >= 3) {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("release requires memx >= 0.3.0 at %s; run scripts/sync-local-builtins to rebuild the cache", expected)
}
