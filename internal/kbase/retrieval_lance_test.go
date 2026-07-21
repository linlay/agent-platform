package kbase

import (
	"math"
	"strings"
	"testing"
)

func TestRetrievalCandidateLimitUsesRequestedWindowAndBounds(t *testing.T) {
	tests := []struct {
		name string
		req  RetrievalRequest
		want int
	}{
		{
			name: "floor",
			req:  RetrievalRequest{Limit: 5, CandidateFloor: 30, CandidateMultiplier: 2, CandidateMax: 500},
			want: 30,
		},
		{
			name: "offset participates in requested window",
			req:  RetrievalRequest{Offset: 5, Limit: 10, CandidateFloor: 30, CandidateMultiplier: 4, CandidateMax: 500},
			want: 60,
		},
		{
			name: "candidate max",
			req:  RetrievalRequest{Offset: 1000, Limit: 50, CandidateFloor: 30, CandidateMultiplier: 4, CandidateMax: 500},
			want: 500,
		},
		{
			name: "hard candidate max cap",
			req:  RetrievalRequest{Offset: 1000, Limit: 50, CandidateFloor: 30, CandidateMultiplier: 4, CandidateMax: 5000},
			want: 2000,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := retrievalCandidateLimit(test.req); got != test.want {
				t.Fatalf("retrievalCandidateLimit() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestFuseWeightedRRFScoreAndMatchTypes(t *testing.T) {
	shared := chunkRecord{ID: "shared", Path: "shared.md", StartLine: 1}
	vectorOnly := chunkRecord{ID: "vector", Path: "vector.md", StartLine: 1}
	ftsOnly := chunkRecord{ID: "fts", Path: "fts.md", StartLine: 1}
	response := fuseWeightedRRF(
		[]rankedChunk{{Chunk: shared}, {Chunk: vectorOnly}},
		[]rankedChunk{{Chunk: shared}, {Chunk: ftsOnly}},
		RetrievalRequest{
			Limit:          10,
			RRFK:           60,
			VectorWeight:   0.7,
			FTSWeight:      0.3,
			CandidateFloor: 10,
			CandidateMax:   100,
		},
	)
	if response.MatchCount != 3 || len(response.Matches) != 3 {
		t.Fatalf("unexpected response counts: %#v", response)
	}
	byID := map[string]RetrievalMatch{}
	for _, match := range response.Matches {
		byID[match.Chunk.ID] = match
	}
	assertFloatNear(t, byID["shared"].Score, 1)
	assertFloatNear(t, byID["vector"].Score, 0.7*61.0/62.0)
	assertFloatNear(t, byID["fts"].Score, 0.3*61.0/62.0)
	if byID["shared"].MatchType != "hybrid" {
		t.Fatalf("shared matchType = %q, want hybrid", byID["shared"].MatchType)
	}
	if byID["vector"].MatchType != "vector" {
		t.Fatalf("vector matchType = %q, want vector", byID["vector"].MatchType)
	}
	if byID["fts"].MatchType != "fts" {
		t.Fatalf("fts matchType = %q, want fts", byID["fts"].MatchType)
	}
}

func TestFuseWeightedRRFPaginatesAndReportsUnion(t *testing.T) {
	vector := []rankedChunk{
		{Chunk: chunkRecord{ID: "a", Path: "a.md"}},
		{Chunk: chunkRecord{ID: "b", Path: "b.md"}},
		{Chunk: chunkRecord{ID: "c", Path: "c.md"}},
		{Chunk: chunkRecord{ID: "d", Path: "d.md"}},
	}
	response := fuseWeightedRRF(vector, nil, RetrievalRequest{
		Offset:              1,
		Limit:               2,
		RRFK:                60,
		VectorWeight:        1,
		FTSWeight:           0,
		CandidateFloor:      30,
		CandidateMax:        100,
		CandidateMultiplier: 4,
	})
	if response.MatchCount != 4 {
		t.Fatalf("matchCount = %d, want 4", response.MatchCount)
	}
	if len(response.Matches) != 2 || response.Matches[0].Chunk.ID != "b" || response.Matches[1].Chunk.ID != "c" {
		t.Fatalf("unexpected page: %#v", response.Matches)
	}
	if !response.Truncated {
		t.Fatal("truncated = false, want true when more fused results remain")
	}
	if response.VectorHits != 4 || response.FTSHits != 0 {
		t.Fatalf("candidate counts = vector:%d fts:%d, want 4/0", response.VectorHits, response.FTSHits)
	}
}

func TestFuseWeightedRRFMarksCandidatePoolSaturationAsTruncated(t *testing.T) {
	vector := make([]rankedChunk, 30)
	for index := range vector {
		vector[index] = rankedChunk{Chunk: chunkRecord{ID: "chunk-" + string(rune('a'+index)), Path: "docs/all.md", StartLine: index + 1}}
	}
	response := fuseWeightedRRF(vector, nil, RetrievalRequest{
		Limit:          30,
		VectorWeight:   1,
		CandidateFloor: 30,
		CandidateMax:   30,
	})
	if len(response.Matches) != 30 || response.MatchCount != 30 {
		t.Fatalf("response counts = page:%d match:%d, want 30/30", len(response.Matches), response.MatchCount)
	}
	if !response.Truncated {
		t.Fatal("truncated = false, want true when a retrieval branch reaches candidateMax")
	}
}

func TestFuseWeightedRRFStableTieOrder(t *testing.T) {
	vector := []rankedChunk{
		{Chunk: chunkRecord{ID: "id-z", Path: "b.md", StartLine: 1}},
		{Chunk: chunkRecord{ID: "id-b", Path: "a.md", StartLine: 20}},
		{Chunk: chunkRecord{ID: "id-c", Path: "a.md", StartLine: 10}},
		{Chunk: chunkRecord{ID: "id-a", Path: "a.md", StartLine: 10}},
	}
	// A zero vector weight makes all vector-only candidates tie at zero. The
	// documented path/start-line/chunk-id ordering must still be deterministic.
	response := fuseWeightedRRF(vector, nil, RetrievalRequest{
		Limit:          10,
		VectorWeight:   0,
		FTSWeight:      1,
		CandidateFloor: 30,
		CandidateMax:   100,
	})
	want := []string{"id-a", "id-c", "id-b", "id-z"}
	for index, match := range response.Matches {
		if match.Chunk.ID != want[index] {
			t.Fatalf("matches[%d] = %q, want %q; all=%#v", index, match.Chunk.ID, want[index], response.Matches)
		}
	}
}

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

func assertFloatNear(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("got %.15f, want %.15f", got, want)
	}
}
