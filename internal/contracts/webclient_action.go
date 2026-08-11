package contracts

import (
	"context"
	"encoding/json"
	"errors"
)

var (
	ErrWebClientTargetUnavailable = errors.New("webclient target unavailable")
	ErrWebClientDisconnected      = errors.New("webclient disconnected")
)

// WebClientTarget is runtime-only routing metadata for the WebClient surface
// that originated a root run. It must never be persisted or exposed to the
// model.
type WebClientTarget struct {
	SessionID   string
	BoundaryKey string
	Subject     string
	SurfaceID   string
}

func (t WebClientTarget) IsZero() bool {
	return t.SessionID == "" && (t.BoundaryKey == "" || t.SurfaceID == "")
}

type WebClientActionRequest struct {
	ID      string
	Type    string
	Payload map[string]any
}

type WebClientActionResponse struct {
	Frame string
	Type  string
	ID    string
	Code  *int
	Msg   string
	Data  json.RawMessage
}

type WebClientRequestInvoker interface {
	InvokeWebClientAction(
		ctx context.Context,
		target WebClientTarget,
		request WebClientActionRequest,
	) (WebClientActionResponse, error)
}

// WebClientTargetStore keeps the latest runtime-only WebClient action target
// for a root run. Bind operations are last-writer-wins; zero targets never
// replace an existing binding.
type WebClientTargetStore interface {
	BindWebClientTarget(runID string, target WebClientTarget) bool
	ResolveWebClientTarget(runID string) (WebClientTarget, bool)
}
