package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/apperrors"
)

func TestTraceStreamRetainsBoundedHeadAndTailWithoutContent(t *testing.T) {
	trace := &llmChatTrace{enabled: true, payload: map[string]any{}}
	for i := 0; i < 200; i++ {
		trace.recordStreamRead("response.output_item.done", `{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"type":"reasoning","status":"completed","encrypted_content":"PRIVATE_CIPHER","arguments":"PRIVATE_ARGUMENT"},"response":{"id":"resp1","status":"in_progress","tools":[{"description":"PRIVATE_TOOL"}]},"PRIVATE_KEY":"PRIVATE_BODY"}`, nil)
		trace.markStreamEvent("handled")
	}
	trace.recordStreamRead("", "", io.EOF)
	trace.markStreamEvent("failed") // EOF must not mislabel the last valid event.
	snap := trace.streamEvents.snapshot()
	frames := snap["frames"].([]traceStreamEvent)
	if len(frames) != 64 || frames[15].Number != 16 || frames[16].Number != 153 || frames[63].Number != 200 || snap["omittedFrameCount"] != 136 || snap["readOutcome"] != "eof" {
		t.Fatalf("bad retention: %#v", snap)
	}
	if frames[63].Processing != "handled" || frames[0].ResponseToolCount != 1 || frames[0].ItemEncryptedBytes != len("PRIVATE_CIPHER") || frames[0].SequenceNumber == nil {
		t.Fatalf("missing structure: %#v", frames[0])
	}
	data, _ := json.Marshal(snap)
	if strings.Contains(string(data), "PRIVATE") {
		t.Fatalf("leaked frame contents: %s", data)
	}
}

func TestResponsesEOFTraceRecordsIgnoredAndHandledFrames(t *testing.T) {
	_, s := responseTestStream()
	path := filepath.Join(t.TempDir(), "trace.json")
	trace := &llmChatTrace{enabled: true, path: path, payload: map[string]any{}}
	s.currentTurn.trace = trace
	wire := `data: {"type":"response.created","response":{"id":"resp1","status":"in_progress"}}` + "\n\n" +
		`data: {"type":"response.output_item.added","item":{"type":"reasoning"}}` + "\n\n" +
		`data: {"type":"response.vendor_unknown","delta":"PRIVATE_TEXT"}` + "\n\n"
	s.currentTurn.reader = bufio.NewReader(strings.NewReader(wire))
	for i := 0; i < 3; i++ {
		if _, err := s.consumeCurrentTurn(); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.consumeCurrentTurn()
	if err == nil {
		t.Fatal("expected EOF failure")
	}
	if err := s.handleModelAttemptError(err); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		StreamEvents struct {
			ReadOutcome string             `json:"readOutcome"`
			Frames      []traceStreamEvent `json:"frames"`
		} `json:"streamEvents"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	frames := record.StreamEvents.Frames
	if len(frames) != 3 || frames[0].Processing != "observed" || frames[1].Processing != "handled" || frames[2].Type != "response.vendor_unknown" || frames[2].Processing != "ignored" || record.StreamEvents.ReadOutcome != "eof" {
		t.Fatalf("%s", data)
	}
	if strings.Contains(string(data), "PRIVATE_TEXT") {
		t.Fatalf("raw frame leaked: %s", data)
	}
}

func TestTraceRetryArchivesHTTPFailureThenStreamFailureThenSuccess(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded"}}`)
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	engine := newTraceTestEngine(t, dir, server.URL, nil)
	req := api.QueryRequest{ChatID: "chat_1", Message: "hello"}
	session := traceTestSession()
	session.ResolvedBudget.Model.RetryCount = 2
	stream, err := engine.newRunStream(context.Background(), req, traceTestSessionWithSystemCache(t, engine, req, session), false)
	if err != nil {
		t.Fatal(err)
	}
	drainTraceTestStream(t, stream)
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
	for i, code := range []string{"provider_rate_limited", "provider_stream_failed", ""} {
		name := []string{"run_trace.attempt-001_001.json", "run_trace.attempt-002_001.json", "run_trace.attempt-003_001.json"}[i]
		data, err := os.ReadFile(filepath.Join(dir, "chat_1", ".llm-records", name))
		if err != nil {
			t.Fatal(err)
		}
		var record map[string]any
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record["attempt"] != float64(i+1) || record["maxAttempts"] != float64(3) {
			t.Fatalf("bad attempt: %s", data)
		}
		diagnostics := record["diagnostics"].(map[string]any)
		if code != "" && diagnostics["errorCode"] != code {
			t.Fatalf("lost previous failure: %s", data)
		}
		if i == 2 && record["status"] != "ok" {
			t.Fatalf("%s", data)
		}
	}
	latest := readTraceFile(t, dir, 1)
	if latest["attempt"] != float64(3) || latest["status"] != "ok" {
		t.Fatalf("compat entry not latest: %#v", latest)
	}
}

func TestTraceStreamRecordsMalformedFrameAndTerminalEvent(t *testing.T) {
	for _, raw := range []string{`{"type":"response.completed","response":{"status":"completed","output":[]}}`, `{broken`} {
		trace := &llmChatTrace{enabled: true, path: filepath.Join(t.TempDir(), "trace.json"), payload: map[string]any{}}
		trace.recordStreamRead("", raw, nil)
		trace.markStreamEvent("handled")
		trace.completeError(apperrors.New(apperrors.CodeProviderStreamInvalid, "test"))
		trace.markStreamEvent("failed")
		snap := trace.streamEvents.snapshot()
		frames := snap["frames"].([]traceStreamEvent)
		if frames[0].Processing != "failed" {
			t.Fatal("lost processing result")
		}
		if strings.Contains(raw, "completed") && snap["terminalEventSeen"] != true {
			t.Fatal("lost terminal event")
		}
		if raw == `{broken` && !frames[0].InvalidJSON {
			t.Fatal("lost malformed event")
		}
	}
}
