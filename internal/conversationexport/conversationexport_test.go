package conversationexport

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

func buildSnapshotForTest(summary *chat.Summary, events []stream.EventData, capturedAt int64) (SnapshotV1, error) {
	document, err := BuildSnapshotDocument(summary, events, capturedAt)
	return document.Snapshot, err
}

const testEpoch = int64(1_700_000_000_000)

func TestBuildSnapshotProjectsVisibleRootTurns(t *testing.T) {
	snapshot, err := buildSnapshotForTest(&chat.Summary{ChatName: " Export ", CreatedAt: testEpoch}, []stream.EventData{
		{Type: "request.query", Timestamp: testEpoch + 10, Payload: map[string]any{"role": "automation", "message": "hidden", "runId": "run-hidden"}},
		{Type: "request.query", Timestamp: testEpoch + 100, Payload: map[string]any{"role": "user", "message": "question", "runId": "run-1"}},
		{Type: "run.start", Timestamp: testEpoch + 90, Payload: map[string]any{"runId": "run-1"}},
		{Type: "reasoning.snapshot", Timestamp: testEpoch + 200, Payload: map[string]any{"runId": "run-1", "text": "reasoning", "reasoningLabel": "analysis"}},
		{Type: "content.snapshot", Timestamp: testEpoch + 300, Payload: map[string]any{"runId": "run-1", "text": "progress"}},
		{Type: "content.snapshot", Timestamp: testEpoch + 400, Payload: map[string]any{"runId": "run-1", "taskId": "private-task", "text": "private"}},
		{Type: "content.snapshot", Timestamp: testEpoch + 500, Payload: map[string]any{"runId": "run-1", "text": "final"}},
		{Type: "run.complete", Timestamp: testEpoch + 600, Payload: map[string]any{"runId": "run-1"}},
	}, testEpoch+1_000)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if snapshot.Title != "Export" || len(snapshot.Turns) != 1 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	turn := snapshot.Turns[0]
	if turn.StartedAt != testEpoch+90 || turn.Outcome != OutcomeCompleted || turn.EndedAt == nil || *turn.EndedAt != testEpoch+600 {
		t.Fatalf("unexpected turn lifecycle: %#v", turn)
	}
	if len(turn.Items) != 4 || turn.Items[0].Kind != ItemUser || turn.Items[1].Kind != ItemReasoning || turn.Items[3].Text != "final" {
		t.Fatalf("unexpected turn items: %#v", turn.Items)
	}
	for _, item := range turn.Items {
		if item.Text == "private" || item.Text == "hidden" {
			t.Fatalf("private item leaked: %#v", turn.Items)
		}
	}
}

func TestBuildSnapshotBindsPendingQueryAndKeepsRunning(t *testing.T) {
	snapshot, err := buildSnapshotForTest(&chat.Summary{ChatName: "Pending", CreatedAt: testEpoch}, []stream.EventData{
		{Type: "request.query", Timestamp: testEpoch + 100, Payload: map[string]any{"role": "user", "message": "question"}},
		{Type: "run.start", Timestamp: testEpoch + 110, Payload: map[string]any{"runId": "run-1"}},
		{Type: "reasoning.snapshot", Timestamp: testEpoch + 120, Payload: map[string]any{"runId": "run-1", "text": "working"}},
	}, testEpoch+200)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if len(snapshot.Turns) != 1 || snapshot.Turns[0].Outcome != OutcomeRunning || snapshot.Turns[0].EndedAt != nil {
		t.Fatalf("unexpected running snapshot: %#v", snapshot)
	}
}

func TestBuildSnapshotMapsTerminalOutcomes(t *testing.T) {
	for eventType, want := range map[string]Outcome{
		"run.complete": OutcomeCompleted,
		"run.cancel":   OutcomeCancelled,
		"run.error":    OutcomeFailed,
	} {
		t.Run(eventType, func(t *testing.T) {
			snapshot, err := buildSnapshotForTest(&chat.Summary{ChatName: "Outcome", CreatedAt: testEpoch}, []stream.EventData{
				{Type: "request.query", Timestamp: testEpoch + 100, Payload: map[string]any{"message": "question", "runId": "run-1"}},
				{Type: eventType, Timestamp: testEpoch + 200, Payload: map[string]any{"runId": "run-1"}},
			}, testEpoch+300)
			if err != nil || len(snapshot.Turns) != 1 || snapshot.Turns[0].Outcome != want {
				t.Fatalf("snapshot=%#v err=%v", snapshot, err)
			}
		})
	}
}

func TestBuildSnapshotDocumentReturnsCanonicalSafeJSON(t *testing.T) {
	document, err := BuildSnapshotDocument(
		&chat.Summary{ChatName: "</script>&x\u2028\u2029x", CreatedAt: testEpoch},
		[]stream.EventData{
			{Type: "request.query", Timestamp: testEpoch + 1, Payload: map[string]any{"message": "<question>", "runId": "run-1"}},
			{Type: "run.complete", Timestamp: testEpoch + 2, Payload: map[string]any{"runId": "run-1"}},
		},
		testEpoch+3,
	)
	if err != nil {
		t.Fatalf("build snapshot document: %v", err)
	}
	if bytes.Contains(document.JSON, []byte("</script>")) ||
		!bytes.Contains(document.JSON, []byte(`\u003c/script\u003e\u0026x\u2028\u2029x`)) {
		t.Fatalf("snapshot JSON is unsafe for transport: %s", document.JSON)
	}
	var decoded SnapshotV1
	if err := json.Unmarshal(document.JSON, &decoded); err != nil {
		t.Fatalf("decode snapshot JSON: %v", err)
	}
	if decoded.Title != document.Snapshot.Title || len(decoded.Turns) != 1 {
		t.Fatalf("JSON and snapshot differ: decoded=%#v snapshot=%#v", decoded, document.Snapshot)
	}
}

func TestBuildSnapshotValidatesEnvelopeTimes(t *testing.T) {
	if _, err := buildSnapshotForTest(&chat.Summary{ChatName: "Bad", CreatedAt: 0}, nil, testEpoch); !timecontract.IsViolation(err) {
		t.Fatalf("createdAt err=%v", err)
	}
	if _, err := buildSnapshotForTest(&chat.Summary{ChatName: "Bad", CreatedAt: testEpoch}, nil, testEpoch-1); !errors.Is(err, ErrInvalidTimeline) {
		t.Fatalf("capturedAt ordering err=%v", err)
	}
	if _, err := buildSnapshotForTest(&chat.Summary{ChatName: "Bad", CreatedAt: testEpoch}, []stream.EventData{{
		Type: "request.query", Timestamp: 0, Payload: map[string]any{"message": "question", "runId": "run-1"},
	}}, testEpoch+1); !timecontract.IsViolation(err) {
		t.Fatalf("event timestamp err=%v", err)
	}
}

func TestBuildSnapshotFailsEarlyWhenProjectedTextExceedsJSONLimit(t *testing.T) {
	events := make([]stream.EventData, 0, 42)
	for index := 0; index < 21; index++ {
		runID := "run-" + string(rune('a'+index))
		events = append(events,
			stream.EventData{Type: "request.query", Timestamp: testEpoch + int64(index*10+1), Payload: map[string]any{"message": strings.Repeat("q", 1024*1024), "runId": runID}},
			stream.EventData{Type: "run.complete", Timestamp: testEpoch + int64(index*10+2), Payload: map[string]any{"runId": runID}},
		)
	}
	if _, err := buildSnapshotForTest(&chat.Summary{ChatName: "Large", CreatedAt: testEpoch}, events, testEpoch+1_000); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized projected text err=%v", err)
	} else {
		var sizeErr *SizeLimitError
		if !errors.As(err, &sizeErr) || sizeErr.Actual <= MaxSnapshotBytes || sizeErr.Limit != MaxSnapshotBytes {
			t.Fatalf("oversized projected text details err=%v", err)
		}
	}
}

func TestBuildSnapshotAllowsLargeItemWithinDocumentLimit(t *testing.T) {
	message := strings.Repeat("q", 201*1024)
	snapshot, err := buildSnapshotForTest(&chat.Summary{ChatName: "Large item", CreatedAt: testEpoch}, []stream.EventData{
		{Type: "request.query", Timestamp: testEpoch + 1, Payload: map[string]any{"message": message, "runId": "run-1"}},
		{Type: "run.complete", Timestamp: testEpoch + 2, Payload: map[string]any{"runId": "run-1"}},
	}, testEpoch+3)
	if err != nil || snapshot.Turns[0].Items[0].Text != message {
		t.Fatalf("snapshot item bytes=%d err=%v", len(snapshot.Turns[0].Items[0].Text), err)
	}
}

func TestBuildSnapshotEnforcesTwoThousandItemLimit(t *testing.T) {
	buildEvents := func(assistantItems int) []stream.EventData {
		events := make([]stream.EventData, 0, assistantItems+2)
		events = append(events, stream.EventData{
			Type:      "request.query",
			Timestamp: testEpoch + 1,
			Payload:   map[string]any{"message": "question", "runId": "run-1"},
		})
		for index := 0; index < assistantItems; index++ {
			events = append(events, stream.EventData{
				Type:      "content.snapshot",
				Timestamp: testEpoch + int64(index) + 2,
				Payload:   map[string]any{"runId": "run-1", "text": "answer"},
			})
		}
		return append(events, stream.EventData{
			Type:      "run.complete",
			Timestamp: testEpoch + int64(assistantItems) + 2,
			Payload:   map[string]any{"runId": "run-1"},
		})
	}

	snapshot, err := buildSnapshotForTest(
		&chat.Summary{ChatName: "Item limit", CreatedAt: testEpoch},
		buildEvents(MaxItems-1),
		testEpoch+MaxItems+2,
	)
	if err != nil || len(snapshot.Turns) != 1 || len(snapshot.Turns[0].Items) != MaxItems {
		t.Fatalf("2000-item snapshot items=%d err=%v", len(snapshot.Turns[0].Items), err)
	}

	if _, err := buildSnapshotForTest(
		&chat.Summary{ChatName: "Item limit", CreatedAt: testEpoch},
		buildEvents(MaxItems),
		testEpoch+MaxItems+3,
	); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("2001-item snapshot err=%v", err)
	}
}

func TestBuildSnapshotRejectsInvalidTimeline(t *testing.T) {
	for name, events := range map[string][]stream.EventData{
		"duplicate run start": {
			{Type: "request.query", Timestamp: testEpoch + 100, Payload: map[string]any{"message": "q", "runId": "run-1"}},
			{Type: "run.start", Timestamp: testEpoch + 101, Payload: map[string]any{"runId": "run-1"}},
			{Type: "run.start", Timestamp: testEpoch + 102, Payload: map[string]any{"runId": "run-1"}},
		},
		"time regression": {
			{Type: "request.query", Timestamp: testEpoch + 100, Payload: map[string]any{"message": "q", "runId": "run-1"}},
			{Type: "content.snapshot", Timestamp: testEpoch + 90, Payload: map[string]any{"runId": "run-1", "text": "a"}},
		},
		"content after terminal": {
			{Type: "request.query", Timestamp: testEpoch + 100, Payload: map[string]any{"message": "q", "runId": "run-1"}},
			{Type: "run.complete", Timestamp: testEpoch + 110, Payload: map[string]any{"runId": "run-1"}},
			{Type: "content.snapshot", Timestamp: testEpoch + 120, Payload: map[string]any{"runId": "run-1", "text": "late"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildSnapshotForTest(&chat.Summary{ChatName: "Invalid", CreatedAt: testEpoch}, events, testEpoch+500)
			if !errors.Is(err, ErrInvalidTimeline) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestRenderMarkdownKeepsOnlyCompletedFinalAnswers(t *testing.T) {
	ended := testEpoch + 500
	markdown, err := RenderMarkdown(SnapshotV1{Turns: []TurnV1{
		{Outcome: OutcomeCompleted, EndedAt: &ended, Items: []ItemV1{
			{Kind: ItemUser, Text: "question **one**", At: testEpoch + 100},
			{Kind: ItemReasoning, Text: "secret", At: testEpoch + 200},
			{Kind: ItemAssistant, Text: "draft", At: testEpoch + 300},
			{Kind: ItemAssistant, Text: "final ✅", At: testEpoch + 400},
		}},
		{Outcome: OutcomeRunning, Items: []ItemV1{{Kind: ItemUser, Text: "running", At: testEpoch + 600}}},
	}})
	if err != nil {
		t.Fatalf("render markdown: %v", err)
	}
	want := "## 用户问题\n\nquestion **one**\n\n## 助手回答\n\nfinal ✅\n"
	if string(markdown) != want {
		t.Fatalf("markdown mismatch\nwant=%q\n got=%q", want, markdown)
	}
	if bytes.Contains(markdown, []byte("secret")) || bytes.Contains(markdown, []byte("draft")) {
		t.Fatalf("markdown leaked intermediate content: %s", markdown)
	}
}

func TestRenderMarkdownPreservesUnicodeAndCRLF(t *testing.T) {
	ended := testEpoch + 500
	markdown, err := RenderMarkdown(SnapshotV1{Turns: []TurnV1{{
		Outcome: OutcomeCompleted,
		EndedAt: &ended,
		Items: []ItemV1{
			{Kind: ItemUser, Text: "中文 **问题**\r\n第二行", At: testEpoch + 100},
			{Kind: ItemAssistant, Text: "答案 ✅\r\n完成", At: testEpoch + 400},
		},
	}}})
	if err != nil || !strings.Contains(string(markdown), "中文 **问题**\r\n第二行") || !strings.Contains(string(markdown), "答案 ✅\r\n完成") {
		t.Fatalf("markdown=%q err=%v", markdown, err)
	}
}
