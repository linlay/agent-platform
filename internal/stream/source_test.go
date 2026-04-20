package stream

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDispatcherEmitsSourcePublishWithComputedFields(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(SourcePublish{
		PublishID:   "src-a1b2c3",
		RunID:       "run_1",
		TaskID:      "task_1",
		ToolID:      "tool_1",
		Kind:        "ragflow",
		Query:       "办公用品申请流程",
		SourceCount: 99,
		ChunkCount:  99,
		Sources: []Source{
			{
				ID:             "doc_1",
				Name:           "guide.pdf",
				Title:          "Guide",
				Icon:           "ragflow",
				URL:            "https://example.com/guide.pdf",
				CollectionID:   "col_1",
				CollectionName: "Policies",
				ChunkIndexes:   []int{999},
				MinIndex:       999,
				Chunks: []SourceChunk{
					{ChunkID: "chunk_4", Index: 4, Content: "later", Score: 0.5},
					{ChunkID: "chunk_1", Index: 1, Content: "earlier"},
				},
			},
		},
	})

	assertEventTypes(t, events, "source.publish")
	payload := events[0].Data()
	if payload.String("publishId") != "src-a1b2c3" {
		t.Fatalf("expected publishId to be preserved, got %#v", payload)
	}
	if payload.String("runId") != "run_1" || payload.String("taskId") != "task_1" || payload.String("toolId") != "tool_1" {
		t.Fatalf("unexpected routing fields %#v", payload)
	}
	if got, ok := payload.Value("sourceCount").(int); !ok || got != 1 {
		t.Fatalf("expected sourceCount=1, got %#v", payload.Value("sourceCount"))
	}
	if got, ok := payload.Value("chunkCount").(int); !ok || got != 2 {
		t.Fatalf("expected chunkCount=2, got %#v", payload.Value("chunkCount"))
	}

	sources, ok := payload.Value("sources").([]Source)
	if !ok || len(sources) != 1 {
		t.Fatalf("expected one typed source payload, got %#v", payload.Value("sources"))
	}
	if sources[0].MinIndex != 1 {
		t.Fatalf("expected computed minIndex=1, got %#v", sources[0])
	}
	if len(sources[0].ChunkIndexes) != 2 || sources[0].ChunkIndexes[0] != 1 || sources[0].ChunkIndexes[1] != 4 {
		t.Fatalf("expected sorted chunkIndexes, got %#v", sources[0].ChunkIndexes)
	}
	if len(sources[0].Chunks) != 2 || sources[0].Chunks[0].ChunkID != "chunk_1" || sources[0].Chunks[1].ChunkID != "chunk_4" {
		t.Fatalf("expected chunks sorted by index, got %#v", sources[0].Chunks)
	}
}

func TestDispatcherEmitsEmptySourcePublish(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_1",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(SourcePublish{
		PublishID: "src-empty",
		RunID:     "run_1",
		Kind:      "websearch",
		Sources:   nil,
	})

	assertEventTypes(t, events, "source.publish")
	payload := events[0].Data()
	if got, ok := payload.Value("sourceCount").(int); !ok || got != 0 {
		t.Fatalf("expected sourceCount=0, got %#v", payload.Value("sourceCount"))
	}
	if got, ok := payload.Value("chunkCount").(int); !ok || got != 0 {
		t.Fatalf("expected chunkCount=0, got %#v", payload.Value("chunkCount"))
	}
	sources, ok := payload.Value("sources").([]Source)
	if !ok || len(sources) != 0 {
		t.Fatalf("expected empty sources slice, got %#v", payload.Value("sources"))
	}
}

func TestDispatcherSourcePublishFallsBackRunIDAndGeneratesPublishID(t *testing.T) {
	dispatcher := NewDispatcher(StreamRequest{
		RunID:  "run_fallback",
		ChatID: "chat_1",
	})

	events := dispatcher.Dispatch(SourcePublish{
		Kind: "local",
		Sources: []Source{
			{
				ID:   "doc_1",
				Name: "Local note",
			},
		},
	})

	assertEventTypes(t, events, "source.publish")
	payload := events[0].Data()
	publishID := payload.String("publishId")
	if !strings.HasPrefix(publishID, "src-") || len(publishID) <= len("src-") {
		t.Fatalf("expected generated publishId, got %#v", publishID)
	}
	if payload.String("runId") != "run_fallback" {
		t.Fatalf("expected runId fallback to dispatcher request, got %#v", payload)
	}
	if _, exists := payload.Payload["taskId"]; exists {
		t.Fatalf("did not expect empty taskId to be stored, got %#v", payload.Payload)
	}
	if _, exists := payload.Payload["toolId"]; exists {
		t.Fatalf("did not expect empty toolId to be stored, got %#v", payload.Payload)
	}
}

func TestEventDataMarshalsSourcePublishWithContractKeyOrder(t *testing.T) {
	event := NewEvent("source.publish", map[string]any{
		"publishId":   "src-a1b2c3",
		"runId":       "run_1",
		"taskId":      "task_1",
		"toolId":      "tool_1",
		"kind":        "ragflow",
		"query":       "办公用品申请流程",
		"sourceCount": 1,
		"chunkCount":  2,
		"sources": []Source{
			{
				ID:             "doc_1",
				Name:           "guide.pdf",
				Title:          "Guide",
				Icon:           "ragflow",
				URL:            "https://example.com/guide.pdf",
				CollectionID:   "col_1",
				CollectionName: "Policies",
				ChunkIndexes:   []int{1, 4},
				MinIndex:       1,
				Chunks: []SourceChunk{
					{ChunkID: "chunk_1", Index: 1, Content: "earlier"},
					{ChunkID: "chunk_4", Index: 4, Content: "later", Score: 0.5},
				},
			},
		},
	})
	event.Seq = 14

	data, err := json.Marshal(event.Data())
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	text := string(data)
	order := []string{
		`"seq":14`,
		`"type":"source.publish"`,
		`"publishId":"src-a1b2c3"`,
		`"runId":"run_1"`,
		`"taskId":"task_1"`,
		`"toolId":"tool_1"`,
		`"kind":"ragflow"`,
		`"query":"办公用品申请流程"`,
		`"sourceCount":1`,
		`"chunkCount":2`,
		`"sources":[{"id":"doc_1","name":"guide.pdf","title":"Guide","icon":"ragflow","url":"https://example.com/guide.pdf","collectionId":"col_1","collectionName":"Policies","chunkIndexes":[1,4],"minIndex":1,"chunks":[{"chunkId":"chunk_1","index":1,"content":"earlier"},{"chunkId":"chunk_4","index":4,"content":"later","score":0.5}]}]`,
		`"timestamp":`,
	}
	prev := -1
	for _, part := range order {
		idx := strings.Index(text, part)
		if idx < 0 {
			t.Fatalf("expected %q in %s", part, text)
		}
		if idx <= prev {
			t.Fatalf("expected ordered keys in %s", text)
		}
		prev = idx
	}
	if strings.Contains(text, `"score":0,`) {
		t.Fatalf("expected zero score to be omitted, got %s", text)
	}
}
