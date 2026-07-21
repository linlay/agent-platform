package kbase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

type Embedder struct {
	BaseURL      string
	APIKey       string
	Model        string
	Dimension    int
	Timeout      int
	EndpointPath string
	httpClient   *http.Client
}

const defaultEmbeddingBatchSize = 10

func NewEmbedder(baseURL, apiKey, model string, dimension, timeout int) *Embedder {
	if timeout <= 0 {
		timeout = 15
	}
	return &Embedder{
		BaseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:       strings.TrimSpace(apiKey),
		Model:        strings.TrimSpace(model),
		Dimension:    dimension,
		Timeout:      timeout,
		EndpointPath: "/v1/embeddings",
		httpClient:   &http.Client{},
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

func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if e == nil || e.BaseURL == "" || e.Model == "" || e.Dimension <= 0 {
		return nil, fmt.Errorf("kbase embedding provider not configured")
	}
	if len(texts) == 0 {
		return [][]float64{}, nil
	}
	vectors := make([][]float64, len(texts))
	for start := 0; start < len(texts); start += defaultEmbeddingBatchSize {
		end := minInt(start+defaultEmbeddingBatchSize, len(texts))
		batchVectors, err := e.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		copy(vectors[start:end], batchVectors)
	}
	return vectors, nil
}

func (e *Embedder) embedBatch(ctx context.Context, texts []string) ([][]float64, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(e.Timeout)*time.Second)
	defer cancel()

	body, err := json.Marshal(embeddingRequest{
		Model:          e.Model,
		Input:          texts,
		EncodingFormat: "float",
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpointURL(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kbase embedding request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kbase embedding API returned status %d: %s", resp.StatusCode, string(data))
	}
	var decoded embeddingResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, fmt.Errorf("decode kbase embedding response: %w", err)
	}
	vectors := make([][]float64, len(texts))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(vectors) {
			continue
		}
		if len(item.Embedding) != e.Dimension {
			return nil, fmt.Errorf("kbase embedding dimension mismatch: got %d want %d", len(item.Embedding), e.Dimension)
		}
		vectors[item.Index] = item.Embedding
	}
	for i, vector := range vectors {
		if len(vector) == 0 {
			return nil, fmt.Errorf("kbase embedding response missing vector at index %d", i)
		}
	}
	return vectors, nil
}

func (e *Embedder) endpointURL() string {
	endpoint := strings.TrimSpace(e.EndpointPath)
	if endpoint == "" {
		endpoint = "/v1/embeddings"
	}
	if parsed, err := neturl.Parse(endpoint); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return endpoint
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return strings.TrimRight(e.BaseURL, "/") + endpoint
}

func (e *Embedder) EmbedSingle(ctx context.Context, text string) ([]float64, error) {
	vectors, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil, fmt.Errorf("kbase embedding response empty")
	}
	return vectors[0], nil
}

func cosineSimilarity(a, b []float64) float64 {
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
	score := dot / (math.Sqrt(normA) * math.Sqrt(normB))
	if score < 0 {
		return 0
	}
	return score
}
