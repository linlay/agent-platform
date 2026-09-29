package connector

import (
	"errors"
	"fmt"
	"slices"
)

var ErrSelectionConflict = errors.New("selected connectors are mutually exclusive; deselect the conflicting connector first")

// SelectionConflictError carries stable IDs independently of presentation names.
type SelectionConflictError struct {
	ConnectorID             string
	ConflictingConnectorIDs []string
}

func (e *SelectionConflictError) Error() string {
	return fmt.Sprintf("%s: %s conflicts with %v", ErrSelectionConflict, e.ConnectorID, e.ConflictingConnectorIDs)
}
func (e *SelectionConflictError) Unwrap() error { return ErrSelectionConflict }

// ValidateSelection applies package declarations in either direction. It never
// loads unselected packages; references to unavailable packages are harmless.
func ValidateSelection(packages []Package) error {
	// A newly enabled connector is appended to the configured selection.
	for i := len(packages) - 1; i >= 0; i-- {
		candidate := packages[i]
		var conflicts []string
		for _, other := range packages {
			if candidate.ID == other.ID {
				continue
			}
			if slices.Contains(candidate.MutuallyExclusiveWith, other.ID) || slices.Contains(other.MutuallyExclusiveWith, candidate.ID) {
				if !slices.Contains(conflicts, other.ID) {
					conflicts = append(conflicts, other.ID)
				}
			}
		}
		if len(conflicts) != 0 {
			return &SelectionConflictError{ConnectorID: candidate.ID, ConflictingConnectorIDs: conflicts}
		}
	}
	return nil
}
