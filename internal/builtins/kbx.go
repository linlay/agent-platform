package builtins

import "fmt"

// RequireKBXComponent prevents a cache produced before the engine switch from
// being published as a working Platform bundle. VerifyManifest verifies bytes.
func RequireKBXComponent(manifest Manifest) error {
	expected := "bin/kbx"
	if manifest.Platform.OS == "windows" {
		expected += ".exe"
	}
	for _, c := range manifest.Components {
		if c.Name == "kbx" && c.Path == expected && len(c.Tree) == 0 {
			return nil
		}
	}
	return fmt.Errorf("release requires the kbx builtin at %s; run scripts/sync-local-builtins to rebuild the cache", expected)
}
