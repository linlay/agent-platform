package contracts

import (
	"context"
	"encoding/json"
	"errors"
)

var (
	ErrClientTargetUnavailable    = errors.New("client target unavailable")
	ErrClientDisconnected         = errors.New("client disconnected")
	ErrWebClientTargetUnavailable = ErrClientTargetUnavailable
	ErrWebClientDisconnected      = ErrClientDisconnected
)

// ClientTarget is runtime-only routing metadata for the client surface that
// originated a root run. It must never be persisted or exposed to the model.
type ClientTarget struct {
	SessionID   string
	BoundaryKey string
	Subject     string
	SurfaceID   string
}

func (t ClientTarget) IsZero() bool {
	return t.SessionID == "" && (t.BoundaryKey == "" || t.SurfaceID == "")
}

type WebClientTarget = ClientTarget

type ClientRequest struct {
	ID      string
	Type    string
	Payload map[string]any
}

type ClientResponseFrame struct {
	Frame    string          `json:"frame"`
	Type     string          `json:"type,omitempty"`
	ID       string          `json:"id"`
	StreamID string          `json:"streamId,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	LastSeq  int64           `json:"lastSeq,omitempty"`
	Code     *int            `json:"code,omitempty"`
	Msg      string          `json:"msg,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Event    json.RawMessage `json:"event,omitempty"`
}

type ClientRequestInvoker interface {
	InvokeClientRequest(
		ctx context.Context,
		target ClientTarget,
		request ClientRequest,
		onFrame func(ClientResponseFrame) error,
	) error
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

type ClientTargetStore interface {
	BindClientTarget(runID string, target ClientTarget) bool
	ResolveClientTarget(runID string) (ClientTarget, bool)
}
