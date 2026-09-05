// Package orchestration owns ordinary sub-Agent and orchestrated Team
// coordination rules shared by local executors.
package orchestration

import (
	"strings"

	runtimetypes "agent-platform/internal/runtime/types"
)

// DeduplicateReferences preserves input order and treats any stable identity
// match as the same Team input.
func DeduplicateReferences(references []runtimetypes.Reference) []runtimetypes.Reference {
	if len(references) < 2 {
		return append([]runtimetypes.Reference(nil), references...)
	}
	out := make([]runtimetypes.Reference, 0, len(references))
	seen := make(map[string]struct{}, len(references)*3)
	for _, reference := range references {
		keys := ReferenceIdentityKeys(reference)
		duplicate := false
		for _, key := range keys {
			if _, exists := seen[key]; exists {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		for _, key := range keys {
			seen[key] = struct{}{}
		}
		out = append(out, reference)
	}
	return out
}

func ReferenceIdentityKeys(reference runtimetypes.Reference) []string {
	keys := make([]string, 0, 5)
	appendKey := func(prefix string, value string) {
		if value = strings.TrimSpace(value); value != "" {
			keys = append(keys, prefix+value)
		}
	}
	appendKey("id:", reference.ID)
	appendKey("sha256:", reference.SHA256)
	appendKey("path:", reference.Path)
	appendKey("url:", reference.URL)
	if referenceType, name := strings.TrimSpace(reference.Type), strings.TrimSpace(reference.Name); len(keys) == 0 && referenceType != "" && name != "" {
		keys = append(keys, "name:"+referenceType+"\x00"+name)
	}
	return keys
}
