package llm

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"agent-platform/internal/observability"
)

const traceStreamHead = 16
const traceStreamTail = 48

// Trace-only structural evidence. Never retain raw frames, output text,
// tool arguments, encrypted reasoning or arbitrary JSON field names.
type traceStreamEvent struct {
	SequenceNumber     *int64         `json:"sequenceNumber,omitempty"`
	OutputIndex        *int64         `json:"outputIndex,omitempty"`
	Number             int            `json:"number"`
	ElapsedMs          int64          `json:"elapsedMs"`
	SSEEvent           string         `json:"sseEvent,omitempty"`
	Type               string         `json:"type,omitempty"`
	DataBytes          int            `json:"dataBytes"`
	Processing         string         `json:"processing"`
	InvalidJSON        bool           `json:"invalidJSON,omitempty"`
	ResponseID         string         `json:"responseId,omitempty"`
	ResponseStatus     string         `json:"responseStatus,omitempty"`
	ItemType           string         `json:"itemType,omitempty"`
	ItemStatus         string         `json:"itemStatus,omitempty"`
	ItemEncryptedBytes int            `json:"itemEncryptedBytes,omitempty"`
	ItemArgumentBytes  int            `json:"itemArgumentBytes,omitempty"`
	OutputItemCount    int            `json:"outputItemCount,omitempty"`
	ResponseToolCount  int            `json:"responseToolCount,omitempty"`
	FieldBytes         map[string]int `json:"fieldBytes,omitempty"`
	OtherFieldCount    int            `json:"otherFieldCount,omitempty"`
}

type traceStreamEvents struct {
	total             int
	head              []traceStreamEvent
	tail              []traceStreamEvent
	readOutcome       string
	terminalEventSeen bool
	itemAddedCount    int
	itemDoneCount     int
	itemTypes         map[string]int
}

func (s *traceStreamEvents) snapshot() map[string]any {
	frames := append([]traceStreamEvent{}, s.head...)
	frames = append(frames, s.tail...)
	return map[string]any{"frameCount": s.total, "omittedFrameCount": s.total - len(frames), "frames": frames, "readOutcome": s.readOutcome, "terminalEventSeen": s.terminalEventSeen, "outputItemAddedCount": s.itemAddedCount, "outputItemDoneCount": s.itemDoneCount, "doneItemTypes": s.itemTypes}
}

func traceLabel(value string) string {
	value = observability.SanitizeLog(strings.TrimSpace(value))
	if len(value) > 128 {
		value = value[:128]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func (t *llmChatTrace) recordStreamRead(event, raw string, err error) {
	if t == nil || !t.enabled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		switch {
		case errors.Is(err, io.EOF):
			t.streamEvents.readOutcome = "eof"
		case isProviderTimeoutError(err):
			t.streamEvents.readOutcome = "timeout"
		default:
			t.streamEvents.readOutcome = "read_error"
		}
		if t.completed {
			t.writeLocked()
		}
		return
	}
	t.streamEvents.total++
	frame := traceStreamEvent{Number: t.streamEvents.total, SSEEvent: traceLabel(event), DataBytes: len(raw), Processing: "read"}
	if !t.startedAt.IsZero() {
		frame.ElapsedMs = max(0, time.Since(t.startedAt).Milliseconds())
	}
	if raw == "[DONE]" {
		frame.Type = "[DONE]"
	} else if raw != "" {
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &fields) != nil || fields == nil {
			frame.InvalidJSON = true
		} else {
			var kind string
			_ = json.Unmarshal(fields["type"], &kind)
			if kind == "" {
				_ = json.Unmarshal(fields["object"], &kind)
			}
			_ = json.Unmarshal(fields["sequence_number"], &frame.SequenceNumber)
			_ = json.Unmarshal(fields["output_index"], &frame.OutputIndex)
			frame.Type = traceLabel(kind)
			frame.FieldBytes = map[string]int{}
			for _, key := range []string{"object", "type", "response", "item", "delta", "error", "code", "message", "choices", "usage", "output_index", "summary_index", "sequence_number", "content_block", "index"} {
				if v, ok := fields[key]; ok {
					frame.FieldBytes[key] = len(v)
				}
			}
			frame.OtherFieldCount = len(fields) - len(frame.FieldBytes)
			var response struct {
				ID     string            `json:"id"`
				Status string            `json:"status"`
				Output []json.RawMessage `json:"output"`
				Tools  []json.RawMessage `json:"tools"`
			}
			if json.Unmarshal(fields["response"], &response) == nil {
				frame.ResponseID, frame.ResponseStatus = traceLabel(response.ID), traceLabel(response.Status)
				frame.OutputItemCount, frame.ResponseToolCount = len(response.Output), len(response.Tools)
			}
			var item struct {
				Type      string `json:"type"`
				Status    string `json:"status"`
				Encrypted string `json:"encrypted_content"`
				Arguments string `json:"arguments"`
			}
			if json.Unmarshal(fields["item"], &item) == nil {
				frame.ItemType, frame.ItemStatus = traceLabel(item.Type), traceLabel(item.Status)
				frame.ItemEncryptedBytes, frame.ItemArgumentBytes = len(item.Encrypted), len(item.Arguments)
			}
		}
	}
	kind := frame.Type
	if kind == "" {
		kind = frame.SSEEvent
	}
	switch kind {
	case "response.output_item.added":
		t.streamEvents.itemAddedCount++
	case "response.output_item.done":
		t.streamEvents.itemDoneCount++
		if t.streamEvents.itemTypes == nil {
			t.streamEvents.itemTypes = map[string]int{}
		}
		itemType := frame.ItemType
		switch itemType {
		case "message", "reasoning", "function_call":
		default:
			itemType = "other"
		}
		t.streamEvents.itemTypes[itemType]++
	}
	switch kind {
	case "response.completed", "response.incomplete", "response.failed", "message_stop", "[DONE]":
		t.streamEvents.terminalEventSeen = true
	}
	if len(t.streamEvents.head) < traceStreamHead {
		t.streamEvents.head = append(t.streamEvents.head, frame)
	} else {
		if len(t.streamEvents.tail) == traceStreamTail {
			copy(t.streamEvents.tail, t.streamEvents.tail[1:])
			t.streamEvents.tail = t.streamEvents.tail[:traceStreamTail-1]
		}
		t.streamEvents.tail = append(t.streamEvents.tail, frame)
	}
	if t.completed {
		t.writeLocked()
	}
}

func (t *llmChatTrace) markStreamEvent(processing string) {
	if t == nil || !t.enabled {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// A read failure is not a failure to process the preceding successful frame.
	if t.streamEvents.readOutcome != "" {
		return
	}
	frames := t.streamEvents.tail
	if len(frames) == 0 {
		frames = t.streamEvents.head
	}
	if len(frames) == 0 {
		return
	}
	frames[len(frames)-1].Processing = processing
	if t.completed {
		t.writeLocked()
	}
}
