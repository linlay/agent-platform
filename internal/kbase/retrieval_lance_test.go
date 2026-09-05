package kbase

import (
	"math"
	"strings"
	"testing"
)

func TestChunkToLanceWireConvertsAndBuildsFTSText(t *testing.T) {
	chunk := chunkRecord{
		ID:             "chunk-1",
		FileID:         "file-1",
		Path:           "Guide/Intro.MD",
		Heading:        "Overview",
		Content:        "hello world",
		ContentHash:    "hash",
		Embedding:      []float64{1.25, -2.5},
		EmbeddingModel: "embedding-model",
	}
	wire, err := chunkToLanceWire(chunk)
	if err != nil {
		t.Fatalf("chunkToLanceWire: %v", err)
	}
	if wire.EmbeddingDimension != 2 || len(wire.Vector) != 2 {
		t.Fatalf("wire dimension/vector = %d/%v, want 2 values", wire.EmbeddingDimension, wire.Vector)
	}
	if wire.Vector[0] != 1.25 || wire.Vector[1] != -2.5 {
		t.Fatalf("wire vector = %v", wire.Vector)
	}
	if wire.Ext != ".md" {
		t.Fatalf("wire ext = %q, want .md", wire.Ext)
	}
	for _, part := range []string{chunk.Path, chunk.Heading, chunk.Content} {
		if !strings.Contains(wire.FTSText, part) {
			t.Fatalf("ftsText %q does not contain %q", wire.FTSText, part)
		}
	}
}

func TestChunkToLanceWireRejectsInvalidVectors(t *testing.T) {
	tests := []struct {
		name      string
		embedding []float64
		dimension int
		wantText  string
	}{
		{name: "dimension mismatch", embedding: []float64{1, 2}, dimension: 3, wantText: "dimension mismatch"},
		{name: "nan", embedding: []float64{math.NaN()}, dimension: 1, wantText: "invalid embedding value"},
		{name: "positive infinity", embedding: []float64{math.Inf(1)}, dimension: 1, wantText: "invalid embedding value"},
		{name: "negative infinity", embedding: []float64{math.Inf(-1)}, dimension: 1, wantText: "invalid embedding value"},
		{name: "float32 overflow", embedding: []float64{math.MaxFloat64}, dimension: 1, wantText: "overflows float32"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := chunkToLanceWire(chunkRecord{ID: "bad", Path: "bad.md", Embedding: test.embedding, EmbeddingDimension: test.dimension})
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("chunkToLanceWire error = %v, want containing %q", err, test.wantText)
			}
		})
	}
}
