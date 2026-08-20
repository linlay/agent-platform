package conversationexport

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

const testEpoch = int64(1_700_000_000_000)

func TestBuildSnapshotProjectsVisibleRootTurns(t *testing.T) {
	snapshot, err := BuildSnapshot(&chat.Summary{ChatName: " Export ", CreatedAt: testEpoch}, []stream.EventData{
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
	snapshot, err := BuildSnapshot(&chat.Summary{ChatName: "Pending", CreatedAt: testEpoch}, []stream.EventData{
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
			snapshot, err := BuildSnapshot(&chat.Summary{ChatName: "Outcome", CreatedAt: testEpoch}, []stream.EventData{
				{Type: "request.query", Timestamp: testEpoch + 100, Payload: map[string]any{"message": "question", "runId": "run-1"}},
				{Type: eventType, Timestamp: testEpoch + 200, Payload: map[string]any{"runId": "run-1"}},
			}, testEpoch+300)
			if err != nil || len(snapshot.Turns) != 1 || snapshot.Turns[0].Outcome != want {
				t.Fatalf("snapshot=%#v err=%v", snapshot, err)
			}
		})
	}
}

func TestBuildSnapshotValidatesEnvelopeTimes(t *testing.T) {
	if _, err := BuildSnapshot(&chat.Summary{ChatName: "Bad", CreatedAt: 0}, nil, testEpoch); !timecontract.IsViolation(err) {
		t.Fatalf("createdAt err=%v", err)
	}
	if _, err := BuildSnapshot(&chat.Summary{ChatName: "Bad", CreatedAt: testEpoch}, nil, testEpoch-1); !errors.Is(err, ErrInvalidTimeline) {
		t.Fatalf("capturedAt ordering err=%v", err)
	}
	if _, err := BuildSnapshot(&chat.Summary{ChatName: "Bad", CreatedAt: testEpoch}, []stream.EventData{{
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
	if _, err := BuildSnapshot(&chat.Summary{ChatName: "Large", CreatedAt: testEpoch}, events, testEpoch+1_000); !errors.Is(err, ErrTooLarge) {
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
	snapshot, err := BuildSnapshot(&chat.Summary{ChatName: "Large item", CreatedAt: testEpoch}, []stream.EventData{
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

	snapshot, err := BuildSnapshot(
		&chat.Summary{ChatName: "Item limit", CreatedAt: testEpoch},
		buildEvents(MaxItems-1),
		testEpoch+MaxItems+2,
	)
	if err != nil || len(snapshot.Turns) != 1 || len(snapshot.Turns[0].Items) != MaxItems {
		t.Fatalf("2000-item snapshot items=%d err=%v", len(snapshot.Turns[0].Items), err)
	}

	if _, err := BuildSnapshot(
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
			_, err := BuildSnapshot(&chat.Summary{ChatName: "Invalid", CreatedAt: testEpoch}, events, testEpoch+500)
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

func TestHTMLRendererInjectsEscapedJSONOnce(t *testing.T) {
	template := []byte(validHTMLTemplateForTest())
	renderer, err := NewHTMLRenderer(template)
	if err != nil {
		t.Fatalf("new renderer: %v", err)
	}
	html, err := renderer.Render(SnapshotV1{
		Version: 1, Title: "</script><script>alert(1)</script>&\u2028\u2029", CreatedAt: testEpoch, CapturedAt: testEpoch + 1,
		Turns: []TurnV1{},
	}, "http://127.0.0.1:11961/")
	if err != nil {
		t.Fatalf("render html: %v", err)
	}
	if bytes.Contains(html, []byte(TemplateMarker)) || bytes.Contains(html, []byte("</script><script>")) || !bytes.Contains(html, []byte(`\u003c/script\u003e`)) || !bytes.Contains(html, []byte(`\u0026`)) || !bytes.Contains(html, []byte(`\u2028\u2029`)) {
		t.Fatalf("unsafe html injection: %s", html)
	}
	if strings.Count(string(html), `"version":1`) != 1 {
		t.Fatalf("snapshot injected more than once: %s", html)
	}
	if bytes.Contains(html, []byte(AssetOriginMarker)) || !bytes.Contains(html, []byte(`http://127.0.0.1:11961/assets/conversation-export/`)) {
		t.Fatalf("asset origin was not injected: %s", html)
	}
}

func TestHTMLRendererRejectsUnsafeAssetOrigins(t *testing.T) {
	renderer, err := NewHTMLRenderer([]byte(validHTMLTemplateForTest()))
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{
		"",
		"http://share.example.test",
		"https://127.0.0.2:11961",
		"https://demo.localhost:11961",
		"https://0.0.0.0:11961",
		"https://user:pass@share.example.test",
		"https://share.example.test/path",
		"https://share.example.test/?query=1",
	} {
		if _, err := renderer.Render(SnapshotV1{Version: 1}, origin); !errors.Is(err, ErrAssetOriginInvalid) {
			t.Fatalf("origin=%q err=%v", origin, err)
		}
	}
}

func TestHTMLRendererRejectsInlineExecutableAssets(t *testing.T) {
	for name, extra := range map[string]string{
		"style":  `<style>body{color:red}</style>`,
		"script": `<script>alert(1)</script>`,
	} {
		t.Run(name, func(t *testing.T) {
			template := []byte(strings.Replace(validHTMLTemplateForTest(), "</head>", extra+"</head>", 1))
			if _, err := NewHTMLRenderer(template); !errors.Is(err, ErrTemplateInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestHTMLRendererRejectsInvalidExternalAssetContract(t *testing.T) {
	const assetSet = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const otherAssetSet = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	valid := validHTMLTemplateForTest()
	for name, template := range map[string]string{
		"stylesheet without sri":   strings.Replace(valid, ` integrity="sha384-test"`, "", 1),
		"stylesheet hash mismatch": strings.Replace(valid, `/conversation-export/`+assetSet+`/runtime.css`, `/conversation-export/`+otherAssetSet+`/runtime.css`, 1),
		"runtime hash mismatch":    strings.Replace(valid, `/conversation-export/`+assetSet+`/runtime.js`, `/conversation-export/`+otherAssetSet+`/runtime.js`, 1),
		"extra stylesheet":         strings.Replace(valid, "</head>", `<link rel="stylesheet" href="https://share.example.test/extra.css" integrity="sha384-test" crossorigin="anonymous"></head>`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewHTMLRenderer([]byte(template)); !errors.Is(err, ErrTemplateInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func validHTMLTemplateForTest() string {
	const assetSet = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	return `<!doctype html><html><head><meta name="conversation-export-profile" content="conversation-snapshot-json-v1"><meta name="conversation-export-asset-set" content="` + assetSet + `"><meta http-equiv="Content-Security-Policy" content="font-src ` + AssetOriginMarker + `; style-src-elem ` + AssetOriginMarker + `; script-src ` + AssetOriginMarker + `"><link rel="stylesheet" href="` + AssetOriginMarker + `/assets/conversation-export/` + assetSet + `/runtime.css" integrity="sha384-test" crossorigin="anonymous"></head><body><script type="application/json">` + TemplateMarker + `</script><script src="` + AssetOriginMarker + `/assets/conversation-export/` + assetSet + `/runtime.js" integrity="sha384-test" crossorigin="anonymous"></script></body></html>`
}
