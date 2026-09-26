package query

import (
	"agent-platform/internal/contracts"
)

func resolveRunWebClientTarget(runs contracts.RunManager, runID string) contracts.WebClientTarget {
	store, ok := runs.(contracts.WebClientTargetStore)
	if !ok {
		return contracts.WebClientTarget{}
	}
	target, _ := store.ResolveWebClientTarget(runID)
	return target
}
