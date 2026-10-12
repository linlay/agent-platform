package query

import (
	"encoding/json"
	"fmt"

	"agent-platform/internal/contracts"
)

// Wait checkpoints carry counters through a continuation in the same process.
// They never authorize resuming execution after a Platform restart.
func decodeWaitCheckpoint(value any) (contracts.WaitCheckpoint, error) {
	var checkpoint contracts.WaitCheckpoint
	raw, err := json.Marshal(value)
	if err == nil {
		err = json.Unmarshal(raw, &checkpoint)
	}
	if err == nil && checkpoint.StartedAt <= 0 {
		err = fmt.Errorf("missing wait execution checkpoint")
	}
	return checkpoint, err
}
