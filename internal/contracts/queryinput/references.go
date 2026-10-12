package queryinput

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrRemovedReferenceSelection = errors.New(RemovedReferenceSelectionMessage)

const RemovedReferenceSelectionMessage = "mustUseSkills and mustUseKbases have been removed; use references"

func RejectRemovedReferenceKeys(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key := range fields {
		if strings.EqualFold(key, "mustUseSkills") || strings.EqualFold(key, "mustUseKbases") {
			return ErrRemovedReferenceSelection
		}
	}
	return nil
}

func ReferenceIdentity(ref Reference) string {
	kind := strings.ToLower(strings.TrimSpace(ref.Type))
	id := strings.TrimSpace(ref.ID)
	if kind == "skill" {
		id = strings.ToLower(id)
	}
	return kind + "\x00" + id
}

func ResourceIDs(refs []Reference, kind string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, ref := range refs {
		if !strings.EqualFold(strings.TrimSpace(ref.Type), kind) {
			continue
		}
		id := strings.TrimSpace(ref.ID)
		if kind == "skill" {
			id = strings.ToLower(id)
		}
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func MakeReferences(kind string, ids []string) []Reference {
	refs := []Reference{}
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			refs = append(refs, Reference{Type: kind, ID: strings.TrimSpace(id)})
		}
	}
	result, _ := NormalizeResourceReferences(refs)
	return result
}

func NormalizeResourceReferences(refs []Reference) ([]Reference, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]Reference, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		ref.Type = strings.ToLower(strings.TrimSpace(ref.Type))
		switch ref.Type {
		case "skill", "kbase", "chat":
			ref.ID = strings.TrimSpace(ref.ID)
			if ref.Type == "skill" {
				ref.ID = strings.ToLower(ref.ID)
			}
			if ref.ID == "" {
				return nil, fmt.Errorf("%s reference id is required", ref.Type)
			}
			key := ReferenceIdentity(ref)
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, ref)
	}
	return out, nil
}

// LocatorReferences is used by management tools: callers provide only resource
// identities, never paths or prepared grants.
func LocatorReferences(value any, allowChat bool) ([]Reference, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var refs []Reference
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, fmt.Errorf("references must be an array: %w", err)
	}
	for index, ref := range refs {
		kind := strings.ToLower(strings.TrimSpace(ref.Type))
		if kind != "skill" && kind != "kbase" && !(allowChat && kind == "chat") {
			return nil, fmt.Errorf("unsupported reference type %q", ref.Type)
		}
		refs[index] = Reference{Type: kind, ID: ref.ID}
	}
	return NormalizeResourceReferences(refs)
}
