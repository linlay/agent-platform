package kbase

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
)

func TestEmbedderBatchesEmbeddingRequests(t *testing.T) {
	var mu sync.Mutex
	var batchSizes []int
	var batches [][]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		mu.Lock()
		batchSizes = append(batchSizes, len(req.Input))
		batches = append(batches, append([]string(nil), req.Input...))
		mu.Unlock()

		data := make([]map[string]any, 0, len(req.Input))
		for i := range req.Input {
			data = append(data, map[string]any{
				"index":     i,
				"embedding": []float64{float64(i), float64(len(req.Input)), 1},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()

	texts := make([]string, 45)
	for i := range texts {
		texts[i] = "chunk"
	}
	embedder := NewEmbedder(server.URL, "test-key", "mock-embedding", 3, 5)

	vectors, err := embedder.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(vectors) != len(texts) {
		t.Fatalf("vector count = %d, want %d", len(vectors), len(texts))
	}
	if got, want := batchSizes, []int{10, 10, 10, 10, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("batch sizes = %#v, want %#v", got, want)
	}
	if len(batches) != 5 {
		t.Fatalf("batch count = %d, want 5", len(batches))
	}
	for i, vector := range vectors {
		batchSize := 10
		batchIndex := i % 10
		if i >= 40 {
			batchSize = 5
			batchIndex = i - 40
		}
		want := []float64{float64(batchIndex), float64(batchSize), 1}
		if !reflect.DeepEqual(vector, want) {
			t.Fatalf("vector[%d] = %#v, want %#v", i, vector, want)
		}
	}
}
