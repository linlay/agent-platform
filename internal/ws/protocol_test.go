package ws

import (
	"encoding/json"
	"testing"

	"agent-platform-runner-go/internal/stream"
)

func TestDecodePayload(t *testing.T) {
	req := RequestFrame{
		Frame:   FrameRequest,
		Type:    "/api/query",
		ID:      "req_1",
		Payload: json.RawMessage(`{"message":"hello"}`),
	}
	payload, err := DecodePayload[struct {
		Message string `json:"message"`
	}](req)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Message != "hello" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestStreamFrameMarshalsEventAndEnd(t *testing.T) {
	eventFrame := StreamFrame{
		Frame:    FrameStream,
		ID:       "req_1",
		StreamID: "s_1",
		Event:    &stream.EventData{Seq: 1, Type: "content.delta"},
	}
	data, err := json.Marshal(eventFrame)
	if err != nil {
		t.Fatalf("marshal event frame: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("expected valid json: %s", string(data))
	}

	endFrame := StreamFrame{
		Frame:    FrameStream,
		ID:       "req_1",
		StreamID: "s_1",
		Reason:   "done",
		LastSeq:  12,
	}
	data, err = json.Marshal(endFrame)
	if err != nil {
		t.Fatalf("marshal end frame: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("expected valid json: %s", string(data))
	}
}
