package stream

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMarkdownDestinationGuardFiltersForbiddenTargetsAcrossChunks(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{
			name:   "transport endpoint",
			chunks: []string{"before ![poster](/api/reso", "urce?file=chat_1%2Fposter.png) after"},
			want:   "before 资源地址不合规 after",
		},
		{
			name:   "current chat prefix",
			chunks: []string{"[download](chat_", "1/artifacts/run_1/report.pdf)"},
			want:   "资源地址不合规",
		},
		{
			name:   "allowed destinations",
			chunks: []string{"![chat](generated%20image.png) [workspace](/Users/alice/project/a.png) ", "[tmp](/tmp/a.png) [web](https://example.com/a.png) ![data](data:image/png;base64,AAAA) ![blob](blob:https://example.com/id)"},
			want:   "![chat](generated%20image.png) [workspace](/Users/alice/project/a.png) [tmp](/tmp/a.png) [web](https://example.com/a.png) ![data](data:image/png;base64,AAAA) ![blob](blob:https://example.com/id)",
		},
		{
			name:   "code is not rewritten",
			chunks: []string{"`![example](/api/resource?file=chat_1%2Fa.png)`\n```md\n", "![example](chat_1/a.png)\n```"},
			want:   "`![example](/api/resource?file=chat_1%2Fa.png)`\n```md\n![example](chat_1/a.png)\n```",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			guard := newMarkdownDestinationGuard("chat_1")
			var got strings.Builder
			for _, chunk := range test.chunks {
				got.WriteString(guard.Write(chunk))
			}
			got.WriteString(guard.Flush())
			if got.String() != test.want {
				t.Fatalf("guard output=%q want=%q", got.String(), test.want)
			}
		})
	}
}

func TestDispatcherNeverEmitsForbiddenMarkdownDestination(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{RunID: "run_1", ChatID: "chat_1"})
	var events []StreamEvent
	events = append(events, dispatcher.Dispatch(ContentDelta{ContentID: "content_1", Delta: "![poster](/api/reso"})...)
	events = append(events, dispatcher.Dispatch(ContentDelta{ContentID: "content_1", Delta: "urce?file=chat_1%2Fposter.png)"})...)
	events = append(events, dispatcher.Dispatch(ToolArgs{ToolID: "tool_1", ToolName: "datetime", Delta: "{}"})...)

	for _, event := range events {
		data, err := json.Marshal(event.Data())
		if err != nil {
			t.Fatal(err)
		}
		encoded := string(data)
		if strings.Contains(encoded, "/api/resource") || strings.Contains(encoded, "chat_1%2Fposter.png") {
			t.Fatalf("forbidden resource destination leaked in %s", encoded)
		}
	}
	var snapshot string
	for _, event := range events {
		if event.Type == "content.snapshot" {
			snapshot = event.Data().String("text")
		}
	}
	if snapshot != rejectedMarkdownResourceText {
		t.Fatalf("content snapshot=%q want=%q", snapshot, rejectedMarkdownResourceText)
	}
}
