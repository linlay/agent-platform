package ws

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-platform-runner-go/internal/stream"
)

const (
	FrameRequest  = "request"
	FrameResponse = "response"
	FrameStream   = "stream"
	FramePush     = "push"
	FrameError    = "error"
)

type RequestFrame struct {
	Frame   string          `json:"frame"`
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type ResponseFrame struct {
	Frame string `json:"frame"`
	Type  string `json:"type"`
	ID    string `json:"id"`
	Code  int    `json:"code"`
	Msg   string `json:"msg"`
	Data  any    `json:"data,omitempty"`
}

type StreamFrame struct {
	Frame    string            `json:"frame"`
	ID       string            `json:"id"`
	StreamID string            `json:"streamId"`
	Event    *stream.EventData `json:"event,omitempty"`
	Reason   string            `json:"reason,omitempty"`
	LastSeq  int64             `json:"lastSeq,omitempty"`
}

type PushFrame struct {
	Frame string `json:"frame"`
	Type  string `json:"type"`
	Data  any    `json:"data,omitempty"`
}

type ErrorFrame struct {
	Frame string `json:"frame"`
	Type  string `json:"type"`
	ID    string `json:"id,omitempty"`
	Code  int    `json:"code"`
	Msg   string `json:"msg"`
	Data  any    `json:"data,omitempty"`
}

type AuthSession struct {
	Context     context.Context
	Subject     string
	ExpiresAt   int64
	Subprotocol string
}

type TokenAuthenticator interface {
	VerifyToken(ctx context.Context, token string) (AuthSession, error)
}

type ProtocolError struct {
	Code int
	Type string
	Msg  string
	Data any
}

func (e *ProtocolError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s (%d): %s", e.Type, e.Code, e.Msg)
}

func DecodePayload[T any](req RequestFrame) (T, error) {
	var value T
	if len(req.Payload) == 0 {
		return value, nil
	}
	if err := json.Unmarshal(req.Payload, &value); err != nil {
		return value, err
	}
	return value, nil
}
