package kbase

import (
	"fmt"
	"strings"
)

// ValidateLibraryID is the dedicated native KBASE requirement. General modes
// retain their optional knowledge capability.
func ValidateLibraryID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("KBASE requires kbaseConfig.libraryId; create or reuse a knowledge library, then bind the ID returned by the platform")
	}
	return nil
}
