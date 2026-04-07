package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// EmbeddingProvider calls an OpenAI-compatible /v1/embeddings endpoint.
type EmbeddingProvider struct {
	BaseURL    string
	APIKey     string
	Model      string
	Dimension  int
	TimeoutMs  int
	httpClient *http.Client
}

func NewEmbeddingProvider(baseURL, apiKey, model string, dimension, timeoutMs int) *EmbeddingProvider {
	if dimension <= 0 {
		dimension = 1024
	}
	if timeoutMs <= 0 {
		timeoutMs = 15000
	}
	return &EmbeddingProvider{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		Model:      model,
		Dimension:  dimension,
		TimeoutMs:  timeoutMs,
		httpClient: &http.Client{},
	}
}

type embeddingRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

// Embed returns embedding vectors for the given texts.
func (p *EmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if p == nil || p.BaseURL == "" || p.Model == "" {
		return nil, fmt.Errorf("embedding provider not configured")
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutMs)*time.Millisecond)
	defer cancel()

	body, err := json.Marshal(embeddingRequest{
		Model:          p.Model,
		Input:          texts,
		EncodingFormat: "float",
	})
	if err != nil {
		return nil, err
	}

	url := p.BaseURL + "/v1/embeddings"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding API returned status %d: %s", resp.StatusCode, string(data))
	}

	var result embeddingResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}

	vectors := make([][]float64, len(result.Data))
	for _, item := range result.Data {
		if len(item.Embedding) != p.Dimension {
			continue // Skip mismatched dimensions
		}
		if item.Index < len(vectors) {
			vectors[item.Index] = item.Embedding
		}
	}
	return vectors, nil
}

// EmbedSingle returns the embedding vector for a single text.
func (p *EmbeddingProvider) EmbedSingle(ctx context.Context, text string) ([]float64, error) {
	vectors, err := p.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 || vectors[0] == nil {
		return nil, fmt.Errorf("no embedding returned")
	}
	return vectors[0], nil
}

// CosineSimilarity computes the cosine similarity between two vectors.
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (sqrt(normA) * sqrt(normB))
}

func sqrt(x float64) float64 {
	if x <= 0 {
		return 0
	}
	// Newton's method
	z := x
	for i := 0; i < 50; i++ {
		z = (z + x/z) / 2
	}
	return z
}
