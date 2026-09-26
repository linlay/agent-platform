package api

import "agent-platform/internal/contracts/queryinput"

func NormalizeSelectionReference(ref Reference) (Reference, error) {
	return queryinput.NormalizeSelectionReference(ref)
}
