package memory

import (
	"math"
	"testing"
	"time"
)

func TestEmbeddingProviderTimeoutUsesSeconds(t *testing.T) {
	provider := NewEmbeddingProvider("http://127.0.0.1:1", "", "embedding-model", 0, 0)
	if provider.Timeout != 15 {
		t.Fatalf("expected default embedding timeout 15 seconds, got %d", provider.Timeout)
	}

	provider = NewEmbeddingProvider("http://127.0.0.1:1", "", "embedding-model", 0, 2)
	if got := time.Duration(provider.Timeout) * time.Second; got != 2*time.Second {
		t.Fatalf("expected configured embedding timeout to be seconds, got %s", got)
	}
}

func TestCosineSimilarityUsesExpectedMagnitude(t *testing.T) {
	got := CosineSimilarity([]float64{1, 2, 3}, []float64{4, 5, 6})
	want := 32 / (math.Sqrt(14) * math.Sqrt(77))
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("expected cosine similarity %.12f, got %.12f", want, got)
	}
}
