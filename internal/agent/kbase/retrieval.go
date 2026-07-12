package kbase

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func normalizedRetrievalRequest(req RetrievalRequest) RetrievalRequest {
	req.PathPrefix = normalizeIndexedPath(req.PathPrefix)
	req.PathGlob = normalizeKBaseGlob(req.PathGlob)
	req.Type = normalizeKBaseExt(req.Type)
	if req.Limit <= 0 {
		req.Limit = 8
	}
	if req.Limit > 50 {
		req.Limit = 50
	}
	if req.Offset < 0 {
		req.Offset = 0
	}
	if req.CandidateFloor < req.Limit {
		req.CandidateFloor = maxInt(30, req.Limit)
	}
	if req.CandidateMultiplier <= 0 {
		req.CandidateMultiplier = 4
	}
	if req.CandidateMax < req.CandidateFloor {
		req.CandidateMax = 500
	}
	if req.CandidateMax > 2000 {
		req.CandidateMax = 2000
	}
	if req.RRFK <= 0 {
		req.RRFK = 60
	}
	if req.VectorWeight < 0 {
		req.VectorWeight = 0
	}
	if req.FTSWeight < 0 {
		req.FTSWeight = 0
	}
	if req.VectorWeight+req.FTSWeight == 0 {
		req.VectorWeight = 0.7
		req.FTSWeight = 0.3
	}
	weightSum := req.VectorWeight + req.FTSWeight
	req.VectorWeight /= weightSum
	req.FTSWeight /= weightSum
	return req
}

func retrievalCandidateLimit(req RetrievalRequest) int {
	req = normalizedRetrievalRequest(req)
	requested := req.Offset + req.Limit
	limit := req.CandidateMultiplier * requested
	if limit < req.CandidateFloor {
		limit = req.CandidateFloor
	}
	if limit > req.CandidateMax {
		limit = req.CandidateMax
	}
	return limit
}

type rankedChunk struct {
	Chunk chunkRecord
	Score float64
}

func fuseWeightedRRF(vector, fts []rankedChunk, req RetrievalRequest) RetrievalResponse {
	req = normalizedRetrievalRequest(req)
	type fused struct {
		chunk      chunkRecord
		vectorRank int
		ftsRank    int
	}
	byID := map[string]*fused{}
	for index, candidate := range vector {
		entry := byID[candidate.Chunk.ID]
		if entry == nil {
			entry = &fused{chunk: candidate.Chunk}
			byID[candidate.Chunk.ID] = entry
		}
		entry.vectorRank = index + 1
	}
	for index, candidate := range fts {
		entry := byID[candidate.Chunk.ID]
		if entry == nil {
			entry = &fused{chunk: candidate.Chunk}
			byID[candidate.Chunk.ID] = entry
		}
		entry.ftsRank = index + 1
	}

	matches := make([]RetrievalMatch, 0, len(byID))
	maxScore := 1.0 / float64(req.RRFK+1)
	for _, entry := range byID {
		raw := 0.0
		matchType := "hybrid"
		if entry.vectorRank > 0 {
			raw += req.VectorWeight / float64(req.RRFK+entry.vectorRank)
		}
		if entry.ftsRank > 0 {
			raw += req.FTSWeight / float64(req.RRFK+entry.ftsRank)
		}
		if entry.vectorRank == 0 {
			matchType = "fts"
		} else if entry.ftsRank == 0 {
			matchType = "vector"
		}
		score := raw / maxScore
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		matches = append(matches, RetrievalMatch{Chunk: entry.chunk, Score: score, MatchType: matchType})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if matches[i].Chunk.Path != matches[j].Chunk.Path {
			return matches[i].Chunk.Path < matches[j].Chunk.Path
		}
		if matches[i].Chunk.StartLine != matches[j].Chunk.StartLine {
			return matches[i].Chunk.StartLine < matches[j].Chunk.StartLine
		}
		return matches[i].Chunk.ID < matches[j].Chunk.ID
	})
	matchCount := len(matches)
	start := minInt(req.Offset, len(matches))
	end := minInt(start+req.Limit, len(matches))
	page := append([]RetrievalMatch(nil), matches[start:end]...)
	return RetrievalResponse{
		Matches:    page,
		MatchCount: matchCount,
		Truncated:  end < len(matches) || len(vector) >= req.CandidateMax || len(fts) >= req.CandidateMax,
		VectorHits: len(vector),
		FTSHits:    len(fts),
	}
}

// SQLiteRetrievalStore preserves the legacy engine as a rollback adapter while
// sharing the new weighted-RRF result contract with LanceDB.
type SQLiteRetrievalStore struct {
	root string
}

func NewSQLiteRetrievalStore(root string) *SQLiteRetrievalStore {
	return &SQLiteRetrievalStore{root: strings.TrimSpace(root)}
}

func (s *SQLiteRetrievalStore) CreateGeneration(context.Context, GenerationSpec) error {
	store, err := OpenStore(s.root)
	if store != nil {
		_ = store.Close()
	}
	return err
}

func (s *SQLiteRetrievalStore) ImportChunks(context.Context, string, []chunkRecord) error {
	return fmt.Errorf("legacy SQLite import requires file records")
}

func (s *SQLiteRetrievalStore) ReplaceFileChunks(_ context.Context, _ string, fileID string, chunks []chunkRecord) (uint64, error) {
	store, err := OpenStore(s.root)
	if err != nil {
		return 0, err
	}
	defer store.Close()
	if len(chunks) == 0 {
		return 0, store.DeleteChunksForFile(fileID)
	}
	return 0, fmt.Errorf("legacy SQLite replace requires file metadata")
}

func (s *SQLiteRetrievalStore) DeleteFileChunks(_ context.Context, _ string, fileID string) (uint64, error) {
	store, err := OpenStore(s.root)
	if err != nil {
		return 0, err
	}
	defer store.Close()
	return 0, store.DeleteChunksForFile(fileID)
}

func (s *SQLiteRetrievalStore) Search(_ context.Context, _ string, req RetrievalRequest) (RetrievalResponse, error) {
	req = normalizedRetrievalRequest(req)
	store, err := OpenReadStore(s.root)
	if err != nil {
		return RetrievalResponse{}, err
	}
	defer store.Close()
	return searchSQLiteStore(store, req)
}

func searchSQLiteStore(store *Store, req RetrievalRequest) (RetrievalResponse, error) {
	req = normalizedRetrievalRequest(req)
	scope := newPathScope(req.PathPrefix, req.PathGlob, req.Type)
	candidateLimit := retrievalCandidateLimit(req)
	ftsHits, err := store.SearchFTS(req.Query, scope, candidateLimit)
	if err != nil {
		return RetrievalResponse{}, err
	}
	fts := make([]rankedChunk, 0, len(ftsHits))
	for _, hit := range ftsHits {
		fts = append(fts, rankedChunk{Chunk: hit.Chunk, Score: hit.Score})
	}
	chunks, err := store.AllChunksWithEmbeddings(scope)
	if err != nil {
		return RetrievalResponse{}, err
	}
	vector := make([]rankedChunk, 0, len(chunks))
	queryVector := make([]float64, len(req.Vector))
	for index, value := range req.Vector {
		queryVector[index] = float64(value)
	}
	for _, chunk := range chunks {
		score := cosineSimilarity(queryVector, chunk.Embedding)
		if !math.IsNaN(score) && !math.IsInf(score, 0) {
			vector = append(vector, rankedChunk{Chunk: chunk, Score: score})
		}
	}
	sort.SliceStable(vector, func(i, j int) bool {
		if vector[i].Score != vector[j].Score {
			return vector[i].Score > vector[j].Score
		}
		return vector[i].Chunk.ID < vector[j].Chunk.ID
	})
	if len(vector) > candidateLimit {
		vector = vector[:candidateLimit]
	}
	return fuseWeightedRRF(vector, fts, req), nil
}

func (s *SQLiteRetrievalStore) ReadChunk(_ context.Context, _ string, id string) (*chunkRecord, error) {
	store, err := OpenReadStore(s.root)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return store.ReadChunk(id)
}

func (s *SQLiteRetrievalStore) ReadPath(_ context.Context, _ string, path string, offset, limit int) ([]chunkRecord, error) {
	store, err := OpenReadStore(s.root)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	rows, err := store.db.Query(`SELECT ID_, FILE_ID_, PATH_, ORDINAL_, HEADING_, START_LINE_, END_LINE_,
		SOURCE_TYPE_, PAGE_START_, PAGE_END_, SLIDE_START_, SLIDE_END_, LOCATOR_JSON_, CONTENT_, CONTENT_HASH_,
		EMBEDDING_, EMBEDDING_MODEL_, EMBEDDING_DIMENSION_, UPDATED_AT_ FROM KBASE_CHUNKS WHERE PATH_=? ORDER BY ORDINAL_`, path)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []chunkRecord
	for rows.Next() {
		chunk, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		if offset > 0 && chunk.EndLine < offset {
			continue
		}
		out = append(out, chunk)
		if limit > 0 && chunk.EndLine >= maxInt(1, offset)+limit-1 {
			break
		}
	}
	return out, rows.Err()
}

func (s *SQLiteRetrievalStore) BuildIndexes(context.Context, string, IndexSpec) error { return nil }
func (s *SQLiteRetrievalStore) WaitForIndexes(context.Context, string, time.Duration) error {
	return nil
}

func (s *SQLiteRetrievalStore) Validate(_ context.Context, _ string) (GenerationValidation, error) {
	store, err := OpenReadStore(s.root)
	if err != nil {
		return GenerationValidation{}, err
	}
	defer store.Close()
	files, chunks, err := store.Counts()
	return GenerationValidation{Ready: err == nil, Files: files, Chunks: chunks, IndexReady: err == nil}, err
}

func (s *SQLiteRetrievalStore) Stats(_ context.Context, _ string) (RetrievalStats, error) {
	store, err := OpenReadStore(s.root)
	if err != nil {
		return RetrievalStats{}, err
	}
	defer store.Close()
	files, chunks, err := store.Counts()
	return RetrievalStats{Files: files, Chunks: chunks, FTSIndexType: "FTS5", VectorIndexType: "flat", FTSReady: err == nil, VectorReady: err == nil}, err
}

func (s *SQLiteRetrievalStore) Optimize(context.Context, string, OptimizeSpec) error { return nil }

var _ RetrievalStore = (*SQLiteRetrievalStore)(nil)
