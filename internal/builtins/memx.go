package builtins

import "fmt"

// RequireMemxComponent rejects release caches predating memx distribution.
// VerifyManifest checks the actual executable and archive-derived license bytes.
func RequireMemxComponent(manifest Manifest) error {
	expected := "bin/memx"
	if manifest.Platform.OS == "windows" {
		expected += ".exe"
	}
	for _, component := range manifest.Components {
		if component.Name == "memx" && component.Path == expected && len(component.Tree) == 0 {
			return nil
		}
	}
	return fmt.Errorf("release requires the memx builtin at %s; run scripts/sync-local-builtins to rebuild the cache", expected)
}
