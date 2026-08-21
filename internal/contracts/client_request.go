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

// ClientTarget is runtime-only routing metadata bound to a root run. It may
// come from the originating client or the Desktop Main default connection,
// and must never be persisted or exposed to the model.
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
	// ErrClientTargetUnavailable means no request was sent and callers may
	// safely resolve a replacement target. ErrClientDisconnected may happen
	// after dispatch, so side-effecting requests must not be replayed.
	InvokeClientRequest(
		ctx context.Context,
		target ClientTarget,
		request ClientRequest,
		onFrame func(ClientResponseFrame) error,
	) error
}

// WebClientTargetStore keeps the latest runtime-only reverse-request target
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

type DesktopMainTargetState string

const (
	DesktopMainTargetMissing      DesktopMainTargetState = "missing"
	DesktopMainTargetDisconnected DesktopMainTargetState = "disconnected"
	DesktopMainTargetReady        DesktopMainTargetState = "ready"
)

// DesktopMainTargetProvider exposes the current authenticated Desktop Main
// reverse-request connection without changing run ownership or surface grants.
type DesktopMainTargetProvider interface {
	ResolveDesktopMainTarget() (ClientTarget, DesktopMainTargetState)
}
