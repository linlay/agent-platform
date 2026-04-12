package memory

import (
	"math"
	"testing"
)

func TestCosineSimilarityUsesExpectedMagnitude(t *testing.T) {
	got := CosineSimilarity([]float64{1, 2, 3}, []float64{4, 5, 6})
	want := 32 / (math.Sqrt(14) * math.Sqrt(77))
	if math.Abs(got-want) > 1e-12 {
		t.Fatalf("expected cosine similarity %.12f, got %.12f", want, got)
	}
}
