package ws

import (
	"encoding/json"

	"agent-platform/internal/testutil"
)

func MarshalPayload(value any) json.RawMessage {
	return testutil.MarshalPayload(value)
}
