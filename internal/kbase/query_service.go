package kbase

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"
)

type queryService struct {
	resolver    *capabilityResolver
	state       *capabilityState
	generations *generationService
	refresh     *refreshCoordinator
	status      *statusService
}

func newQueryService(resolver *capabilityResolver, state *capabilityState, generations *generationService, refresh *refreshCoordinator, status *statusService) *queryService {
	return &queryService{resolver: resolver, state: state, generations: generations, refresh: refresh, status: status}
}

func (s *queryService) Search(ctx context.Context, agentKey, query string, options SearchOptions) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, fmt.Errorf("query must not be blank")
	}
	if err := s.state.DegradedError(agentKey); err != nil {
		return SearchResult{}, err
	}
	cfg, embedder, err := s.resolver.Resolve(agentKey)
	if err != nil {
		return SearchResult{}, err
	}
	status, statusErr := s.status.Status(agentKey)
	limit := options.Limit
	if limit <= 0 {
		limit = cfg.Retrieval.TopK
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 50 {
		limit = 50
	}
	offset := options.Offset
	if offset < 0 {
		offset = 0
	}
	retrieval, generationID, available, err := s.generations.SelectRetrieval(ctx, cfg)
	if err != nil {
		return SearchResult{}, err
	}
	if !available {
		if statusErr == nil && status.Stale {
			s.refresh.QueueRefresh(cfg.AgentKey, cfg.StorageDir, "search")
		}
		return SearchResult{
			AgentKey: cfg.AgentKey, Query: query, Count: 0, Offset: offset, Limit: limit,
			Results: nil, Stale: true, Indexing: statusErr == nil && status.Indexing,
		}, nil
	}
	queryEmbedder, queryDimension, err := s.resolver.EmbedderForRetrieval(ctx, cfg, generationID, embedder)
	if err != nil {
		return SearchResult{}, err
	}
	queryVector, err := queryEmbedder.EmbedSingle(ctx, query)
	if err != nil {
		return SearchResult{}, err
	}
	vector, err := float32Vector(queryVector, queryDimension)
	if err != nil {
		return SearchResult{}, err
	}
	response, err := retrieval.Search(ctx, generationID, RetrievalRequest{
		Query: query, Vector: vector, Limit: limit, Offset: offset,
		CandidateFloor: cfg.Retrieval.CandidateFloor, CandidateMultiplier: cfg.Retrieval.CandidateMultiplier,
		CandidateMax: cfg.Retrieval.CandidateMax, RRFK: cfg.Retrieval.RRFK,
		VectorWeight: cfg.Retrieval.VectorWeight, FTSWeight: cfg.Retrieval.FTSWeight,
		PathPrefix: options.PathPrefix, PathGlob: options.PathGlob, Type: options.Type,
	})
	if err != nil {
		return SearchResult{}, err
	}
	hits := make([]SearchHit, 0, len(response.Matches))
	for _, match := range response.Matches {
		chunk := match.Chunk
		hits = append(hits, SearchHit{
			ChunkID: chunk.ID, Path: chunk.Path, Heading: chunk.Heading,
			StartLine: chunk.StartLine, EndLine: chunk.EndLine, PageStart: chunk.PageStart, PageEnd: chunk.PageEnd,
			SlideStart: chunk.SlideStart, SlideEnd: chunk.SlideEnd, SourceType: chunk.SourceType,
			Snippet: snippet(chunk.Content, query), Score: match.Score, MatchType: match.MatchType,
		})
	}
	result := SearchResult{
		AgentKey: cfg.AgentKey, Query: query, Count: len(hits), MatchCount: response.MatchCount,
		Offset: offset, Limit: limit, Truncated: response.Truncated, Results: hits,
		Stale:    statusErr != nil || status.Stale,
		Indexing: s.refresh.IsIndexing(cfg.AgentKey, cfg.StorageDir) || statusErr == nil && status.Indexing,
	}
	if result.Stale && !result.Indexing {
		s.refresh.QueueRefresh(cfg.AgentKey, cfg.StorageDir, "search")
	}
	return result, nil
}

func (s *queryService) Read(agentKey string, options ReadOptions) (ReadResult, error) {
	if err := s.state.DegradedError(agentKey); err != nil {
		return ReadResult{}, err
	}
	cfg, _, err := s.resolver.Resolve(agentKey)
	if err != nil {
		return ReadResult{}, err
	}
	chunkID := strings.TrimSpace(options.ChunkID)
	path := filepath.ToSlash(strings.TrimSpace(options.Path))
	if chunkID == "" && path == "" {
		return ReadResult{}, fmt.Errorf("chunkId or path is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	retrieval, generationID, available, err := s.generations.SelectRetrieval(ctx, cfg)
	if err != nil {
		return ReadResult{}, err
	}
	if !available {
		return ReadResult{Found: false, ChunkID: chunkID, Path: path}, nil
	}
	if chunkID != "" {
		chunk, err := retrieval.ReadChunk(ctx, generationID, chunkID)
		if err != nil || chunk == nil {
			if err != nil {
				return ReadResult{}, err
			}
			return ReadResult{Found: false, ChunkID: chunkID}, nil
		}
		return ReadResult{
			Found: true, ChunkID: chunk.ID, Path: chunk.Path, Heading: chunk.Heading,
			StartLine: chunk.StartLine, EndLine: chunk.EndLine, PageStart: chunk.PageStart, PageEnd: chunk.PageEnd,
			SlideStart: chunk.SlideStart, SlideEnd: chunk.SlideEnd, SourceType: chunk.SourceType, Content: chunk.Content,
		}, nil
	}
	chunks, err := retrieval.ReadPath(ctx, generationID, path, options.Offset, options.Limit)
	if err != nil {
		return ReadResult{}, err
	}
	return readResultFromChunks(path, options.Offset, chunks), nil
}

func snippet(content, query string) string {
	const maxSnippet = 500
	content = strings.TrimSpace(content)
	if len([]rune(content)) <= maxSnippet {
		return content
	}
	lowerContent := strings.ToLower(content)
	index := -1
	for _, term := range strings.Fields(strings.ToLower(query)) {
		if i := strings.Index(lowerContent, term); i >= 0 {
			index = i
			break
		}
	}
	runes := []rune(content)
	if index < 0 {
		return string(runes[:maxSnippet])
	}
	prefix := len([]rune(content[:index]))
	start := prefix - maxSnippet/3
	if start < 0 {
		start = 0
	}
	end := start + maxSnippet
	if end > len(runes) {
		end = len(runes)
	}
	out := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		out = "..." + out
	}
	if end < len(runes) {
		out += "..."
	}
	return out
}

func readResultFromChunks(path string, offset int, chunks []chunkRecord) ReadResult {
	if len(chunks) == 0 {
		return ReadResult{Found: false, Path: path}
	}
	if offset <= 0 {
		offset = 1
	}
	parts := make([]string, 0, len(chunks))
	result := ReadResult{Found: true, Path: path}
	for index, chunk := range chunks {
		if index == 0 {
			result.StartLine, result.PageStart, result.SlideStart = maxInt(chunk.StartLine, offset), chunk.PageStart, chunk.SlideStart
			result.SourceType = chunk.SourceType
		}
		result.EndLine = chunk.EndLine
		if chunk.PageEnd > 0 {
			result.PageEnd = chunk.PageEnd
		}
		if chunk.SlideEnd > 0 {
			result.SlideEnd = chunk.SlideEnd
		}
		parts = append(parts, chunk.Content)
	}
	result.Content = strings.Join(parts, "\n")
	return result
}

func float32Vector(vector []float64, expected int) ([]float32, error) {
	if len(vector) != expected {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d want %d", len(vector), expected)
	}
	out := make([]float32, len(vector))
	for index, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("query embedding contains NaN or Inf")
		}
		out[index] = float32(value)
	}
	return out, nil
}
