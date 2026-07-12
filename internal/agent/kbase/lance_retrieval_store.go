package kbase

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type LanceRetrievalStore struct {
	process *LanceEngineProcess
	mu      sync.RWMutex
	agents  map[string]string
}

func NewLanceRetrievalStore(process *LanceEngineProcess) *LanceRetrievalStore {
	return &LanceRetrievalStore{process: process, agents: map[string]string{}}
}

func (s *LanceRetrievalStore) agentKey(generationID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agents[generationID]
}

func (s *LanceRetrievalStore) bindGeneration(generationID, agentKey string) {
	s.mu.Lock()
	s.agents[generationID] = agentKey
	s.mu.Unlock()
}

func (s *LanceRetrievalStore) ReleaseGeneration(ctx context.Context, generationID, agentKey string) (bool, error) {
	if s == nil || s.process == nil || !s.process.State().Available {
		return false, nil
	}
	if strings.TrimSpace(agentKey) == "" {
		agentKey = s.agentKey(generationID)
	}
	request := struct{ lanceBaseRequest }{lanceBaseRequest{RequestID: lanceRequestID("release"), AgentKey: agentKey, GenerationID: generationID}}
	var response struct {
		Released bool `json:"released"`
	}
	if err := s.process.doJSON(ctx, "POST", "/v1/generations/release", request, &response, 10*time.Second); err != nil {
		return false, err
	}
	s.mu.Lock()
	delete(s.agents, generationID)
	s.mu.Unlock()
	return response.Released, nil
}

type lanceBaseRequest struct {
	RequestID    string `json:"requestId"`
	AgentKey     string `json:"agentKey"`
	GenerationID string `json:"generationId"`
}

type lanceChunkWire struct {
	ChunkID            string    `json:"chunkId"`
	FileID             string    `json:"fileId"`
	Path               string    `json:"path"`
	Ext                string    `json:"ext"`
	Ordinal            int       `json:"ordinal"`
	Heading            string    `json:"heading"`
	StartLine          int       `json:"startLine"`
	EndLine            int       `json:"endLine"`
	PageStart          int       `json:"pageStart"`
	PageEnd            int       `json:"pageEnd"`
	SlideStart         int       `json:"slideStart"`
	SlideEnd           int       `json:"slideEnd"`
	SourceType         string    `json:"sourceType"`
	LocatorJSON        string    `json:"locatorJson"`
	Content            string    `json:"content"`
	FTSText            string    `json:"ftsText"`
	ContentHash        string    `json:"contentHash"`
	EmbeddingModel     string    `json:"embeddingModel"`
	EmbeddingDimension int       `json:"embeddingDimension"`
	Vector             []float32 `json:"vector,omitempty"`
	UpdatedAt          int64     `json:"updatedAt"`
}

func chunkToLanceWire(chunk chunkRecord) (lanceChunkWire, error) {
	dimension := chunk.EmbeddingDimension
	if dimension <= 0 {
		dimension = len(chunk.Embedding)
	}
	if len(chunk.Embedding) != dimension {
		return lanceChunkWire{}, fmt.Errorf("chunk %s embedding dimension mismatch: got %d want %d", chunk.ID, len(chunk.Embedding), dimension)
	}
	vector := make([]float32, len(chunk.Embedding))
	for index, value := range chunk.Embedding {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return lanceChunkWire{}, fmt.Errorf("chunk %s contains an invalid embedding value", chunk.ID)
		}
		converted := float32(value)
		if math.IsNaN(float64(converted)) || math.IsInf(float64(converted), 0) {
			return lanceChunkWire{}, fmt.Errorf("chunk %s embedding value overflows float32", chunk.ID)
		}
		vector[index] = converted
	}
	ext := strings.ToLower(filepath.Ext(chunk.Path))
	return lanceChunkWire{
		ChunkID:            chunk.ID,
		FileID:             chunk.FileID,
		Path:               chunk.Path,
		Ext:                ext,
		Ordinal:            chunk.Ordinal,
		Heading:            chunk.Heading,
		StartLine:          chunk.StartLine,
		EndLine:            chunk.EndLine,
		PageStart:          chunk.PageStart,
		PageEnd:            chunk.PageEnd,
		SlideStart:         chunk.SlideStart,
		SlideEnd:           chunk.SlideEnd,
		SourceType:         chunk.SourceType,
		LocatorJSON:        chunk.LocatorJSON,
		Content:            chunk.Content,
		FTSText:            strings.Join([]string{chunk.Path, chunk.Heading, chunk.Content}, "\n"),
		ContentHash:        chunk.ContentHash,
		EmbeddingModel:     chunk.EmbeddingModel,
		EmbeddingDimension: dimension,
		Vector:             vector,
		UpdatedAt:          chunk.UpdatedAt,
	}, nil
}

func lanceWireToChunk(wire lanceChunkWire) chunkRecord {
	embedding := make([]float64, len(wire.Vector))
	for index, value := range wire.Vector {
		embedding[index] = float64(value)
	}
	return chunkRecord{
		ID:                 wire.ChunkID,
		FileID:             wire.FileID,
		Path:               wire.Path,
		Ordinal:            wire.Ordinal,
		Heading:            wire.Heading,
		StartLine:          wire.StartLine,
		EndLine:            wire.EndLine,
		PageStart:          wire.PageStart,
		PageEnd:            wire.PageEnd,
		SlideStart:         wire.SlideStart,
		SlideEnd:           wire.SlideEnd,
		SourceType:         wire.SourceType,
		LocatorJSON:        wire.LocatorJSON,
		Content:            wire.Content,
		ContentHash:        wire.ContentHash,
		Embedding:          embedding,
		EmbeddingModel:     wire.EmbeddingModel,
		EmbeddingDimension: wire.EmbeddingDimension,
		UpdatedAt:          wire.UpdatedAt,
	}
}

func lanceRequestID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func (s *LanceRetrievalStore) ensure(ctx context.Context) error {
	if s == nil || s.process == nil {
		return fmt.Errorf("kbase lance retrieval store is not configured")
	}
	return s.process.EnsureStarted(ctx)
}

func (s *LanceRetrievalStore) CreateGeneration(ctx context.Context, spec GenerationSpec) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	requestID := lanceRequestID("create")
	started := time.Now()
	request := struct {
		lanceBaseRequest
		StorageDir      string `json:"storageDir"`
		VectorDimension int    `json:"vectorDimension"`
		EmbeddingModel  string `json:"embeddingModel,omitempty"`
		FTSTokenizer    string `json:"ftsTokenizer,omitempty"`
	}{
		lanceBaseRequest: lanceBaseRequest{RequestID: requestID, AgentKey: spec.AgentKey, GenerationID: spec.GenerationID},
		StorageDir:       spec.StorageDir,
		VectorDimension:  spec.EmbeddingDimension,
		EmbeddingModel:   spec.EmbeddingModel,
		FTSTokenizer:     firstNonBlank(spec.FTSBaseTokenizer, "icu"),
	}
	var response struct {
		GenerationID string `json:"generationId"`
	}
	if err := s.process.doJSON(ctx, "POST", "/v1/generations/create", request, &response, 60*time.Second); err != nil {
		logLanceRequest(spec.AgentKey, spec.GenerationID, "generation.create", requestID, started, "", err)
		return err
	}
	s.bindGeneration(spec.GenerationID, spec.AgentKey)
	logLanceRequest(spec.AgentKey, spec.GenerationID, "generation.create", requestID, started,
		fmt.Sprintf("dimension=%d", spec.EmbeddingDimension), nil)
	return nil
}

func (s *LanceRetrievalStore) ImportChunks(ctx context.Context, generationID string, chunks []chunkRecord) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	wires := make([]lanceChunkWire, len(chunks))
	for index, chunk := range chunks {
		wire, err := chunkToLanceWire(chunk)
		if err != nil {
			return err
		}
		wires[index] = wire
	}
	if len(wires) == 0 {
		return nil
	}
	payload, err := encodeLanceChunksIPC(wires)
	if err != nil {
		return err
	}
	requestID := lanceRequestID("import")
	started := time.Now()
	err = s.process.doArrow(ctx, "/v1/generations/import", payload, s.arrowHeaders(generationID, "", requestID), nil, 30*time.Minute)
	logLanceRequest(s.agentKey(generationID), generationID, "generation.import", requestID, started,
		fmt.Sprintf("chunks=%d", len(wires)), err)
	return err
}

func (s *LanceRetrievalStore) ReplaceFileChunks(ctx context.Context, generationID, fileID string, chunks []chunkRecord) (uint64, error) {
	if err := s.ensure(ctx); err != nil {
		return 0, err
	}
	wires := make([]lanceChunkWire, len(chunks))
	for index, chunk := range chunks {
		wire, err := chunkToLanceWire(chunk)
		if err != nil {
			return 0, err
		}
		wires[index] = wire
	}
	if len(wires) > 0 {
		payload, err := encodeLanceChunksIPC(wires)
		if err != nil {
			return 0, err
		}
		var response struct {
			TableVersion uint64 `json:"tableVersion"`
		}
		requestID := lanceRequestID("replace")
		started := time.Now()
		headers := s.arrowHeaders(generationID, fileID, requestID)
		err = s.process.doArrow(ctx, "/v1/chunks/replace-file", payload, headers, &response, 60*time.Second)
		logLanceRequest(s.agentKey(generationID), generationID, "chunks.replace-file", requestID, started,
			fmt.Sprintf("chunks=%d tableVersion=%d", len(wires), response.TableVersion), err)
		return response.TableVersion, err
	}
	request := struct {
		lanceBaseRequest
		FileID string           `json:"fileId"`
		Chunks []lanceChunkWire `json:"chunks"`
	}{lanceBaseRequest: lanceBaseRequest{RequestID: lanceRequestID("replace"), AgentKey: s.agentKey(generationID), GenerationID: generationID}, FileID: fileID, Chunks: wires}
	var response struct {
		TableVersion uint64 `json:"tableVersion"`
	}
	err := s.process.doJSON(ctx, "POST", "/v1/chunks/replace-file", request, &response, 60*time.Second)
	return response.TableVersion, err
}

func (s *LanceRetrievalStore) arrowHeaders(generationID, fileID, requestID string) map[string]string {
	headers := map[string]string{
		"X-Kbase-Request-Id":    requestID,
		"X-Kbase-Agent-Key":     s.agentKey(generationID),
		"X-Kbase-Generation-Id": generationID,
	}
	if fileID != "" {
		headers["X-Kbase-File-Id"] = fileID
	}
	return headers
}

func (s *LanceRetrievalStore) DeleteFileChunks(ctx context.Context, generationID, fileID string) (uint64, error) {
	if err := s.ensure(ctx); err != nil {
		return 0, err
	}
	request := struct {
		lanceBaseRequest
		FileID string `json:"fileId"`
	}{lanceBaseRequest: lanceBaseRequest{RequestID: lanceRequestID("delete"), AgentKey: s.agentKey(generationID), GenerationID: generationID}, FileID: fileID}
	var response struct {
		TableVersion uint64 `json:"tableVersion"`
	}
	err := s.process.doJSON(ctx, "POST", "/v1/chunks/delete-file", request, &response, 60*time.Second)
	return response.TableVersion, err
}

func (s *LanceRetrievalStore) Search(ctx context.Context, generationID string, req RetrievalRequest) (RetrievalResponse, error) {
	if err := s.ensure(ctx); err != nil {
		return RetrievalResponse{}, err
	}
	req = normalizedRetrievalRequest(req)
	requestID := lanceRequestID("search")
	started := time.Now()
	request := struct {
		lanceBaseRequest
		Query               string    `json:"query"`
		Vector              []float32 `json:"vector"`
		Offset              int       `json:"offset"`
		Limit               int       `json:"limit"`
		RRFK                int       `json:"rrfK"`
		VectorWeight        float64   `json:"vectorWeight"`
		FTSWeight           float64   `json:"ftsWeight"`
		CandidateFloor      int       `json:"candidateFloor"`
		CandidateMultiplier int       `json:"candidateMultiplier"`
		CandidateMax        int       `json:"candidateMax"`
		PathPrefix          string    `json:"pathPrefix,omitempty"`
		PathGlob            string    `json:"pathGlob,omitempty"`
		Type                string    `json:"type,omitempty"`
	}{
		lanceBaseRequest:    lanceBaseRequest{RequestID: requestID, AgentKey: s.agentKey(generationID), GenerationID: generationID},
		Query:               req.Query,
		Vector:              req.Vector,
		Offset:              req.Offset,
		Limit:               req.Limit,
		RRFK:                req.RRFK,
		VectorWeight:        req.VectorWeight,
		FTSWeight:           req.FTSWeight,
		CandidateFloor:      req.CandidateFloor,
		CandidateMultiplier: req.CandidateMultiplier,
		CandidateMax:        req.CandidateMax,
		PathPrefix:          req.PathPrefix,
		PathGlob:            req.PathGlob,
		Type:                req.Type,
	}
	var response struct {
		Matches []struct {
			Chunk     lanceChunkWire `json:"chunk"`
			Score     float64        `json:"score"`
			MatchType string         `json:"matchType"`
		} `json:"matches"`
		MatchCount       int  `json:"matchCount"`
		Truncated        bool `json:"truncated"`
		VectorCandidates int  `json:"vectorCandidates"`
		FTSCandidates    int  `json:"ftsCandidates"`
	}
	if err := s.process.doJSON(ctx, "POST", "/v1/search", request, &response, 30*time.Second); err != nil {
		digest := sha256.Sum256([]byte(req.Query))
		logLanceRequest(s.agentKey(generationID), generationID, "search", requestID, started,
			fmt.Sprintf("queryLength=%d queryHash=%x", len([]rune(req.Query)), digest[:6]), err)
		return RetrievalResponse{}, err
	}
	out := RetrievalResponse{MatchCount: response.MatchCount, Truncated: response.Truncated, VectorHits: response.VectorCandidates, FTSHits: response.FTSCandidates}
	for _, match := range response.Matches {
		out.Matches = append(out.Matches, RetrievalMatch{Chunk: lanceWireToChunk(match.Chunk), Score: match.Score, MatchType: match.MatchType})
	}
	digest := sha256.Sum256([]byte(req.Query))
	logLanceRequest(s.agentKey(generationID), generationID, "search", requestID, started,
		fmt.Sprintf("queryLength=%d queryHash=%x vectorCandidates=%d ftsCandidates=%d count=%d",
			len([]rune(req.Query)), digest[:6], response.VectorCandidates, response.FTSCandidates, len(out.Matches)), nil)
	return out, nil
}

func (s *LanceRetrievalStore) ReadChunk(ctx context.Context, generationID, chunkID string) (*chunkRecord, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	request := struct {
		lanceBaseRequest
		ChunkID string `json:"chunkId"`
	}{lanceBaseRequest: lanceBaseRequest{RequestID: lanceRequestID("read"), AgentKey: s.agentKey(generationID), GenerationID: generationID}, ChunkID: chunkID}
	var response struct {
		Chunk *lanceChunkWire `json:"chunk"`
	}
	if err := s.process.doJSON(ctx, "POST", "/v1/read/chunk", request, &response, 30*time.Second); err != nil {
		return nil, err
	}
	if response.Chunk == nil {
		return nil, nil
	}
	chunk := lanceWireToChunk(*response.Chunk)
	return &chunk, nil
}

func (s *LanceRetrievalStore) ReadPath(ctx context.Context, generationID, path string, offset, limit int) ([]chunkRecord, error) {
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	if offset <= 0 {
		offset = 1
	}
	if limit <= 0 {
		limit = 200
	}
	request := struct {
		lanceBaseRequest
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}{lanceBaseRequest: lanceBaseRequest{RequestID: lanceRequestID("read_path"), AgentKey: s.agentKey(generationID), GenerationID: generationID}, Path: path, Offset: offset, Limit: limit}
	var response struct {
		Chunks []lanceChunkWire `json:"chunks"`
	}
	if err := s.process.doJSON(ctx, "POST", "/v1/read/path", request, &response, 30*time.Second); err != nil {
		return nil, err
	}
	out := make([]chunkRecord, len(response.Chunks))
	for index, wire := range response.Chunks {
		out[index] = lanceWireToChunk(wire)
	}
	return out, nil
}

func (s *LanceRetrievalStore) BuildIndexes(ctx context.Context, generationID string, spec IndexSpec) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	request := struct {
		lanceBaseRequest
		ANNMinRows   int    `json:"annMinRows"`
		FTSTokenizer string `json:"ftsTokenizer"`
	}{lanceBaseRequest: lanceBaseRequest{RequestID: lanceRequestID("index"), AgentKey: s.agentKey(generationID), GenerationID: generationID}, ANNMinRows: spec.ANNMinRows, FTSTokenizer: spec.FTSBaseTokenizer}
	return s.process.doJSON(ctx, "POST", "/v1/indexes/build", request, nil, 30*time.Minute)
}

func (s *LanceRetrievalStore) WaitForIndexes(ctx context.Context, generationID string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		stats, err := s.Stats(ctx, generationID)
		if err == nil && stats.FTSReady && stats.VectorReady {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for KBASE Lance indexes")
		case <-ticker.C:
		}
	}
}

func (s *LanceRetrievalStore) Validate(ctx context.Context, generationID string) (GenerationValidation, error) {
	if err := s.ensure(ctx); err != nil {
		return GenerationValidation{}, err
	}
	request := struct{ lanceBaseRequest }{lanceBaseRequest{RequestID: lanceRequestID("validate"), AgentKey: s.agentKey(generationID), GenerationID: generationID}}
	var response struct {
		Valid             bool              `json:"valid"`
		RowCount          int               `json:"rowCount"`
		FileCount         int               `json:"fileCount"`
		DuplicateChunkIDs int               `json:"duplicateChunkIds"`
		InvalidVectors    int               `json:"invalidVectors"`
		TableVersion      uint64            `json:"tableVersion"`
		ChunkIDDigest     string            `json:"chunkIdDigest"`
		FileIDDigest      string            `json:"fileIdDigest"`
		FileChunkHashes   map[string]string `json:"fileChunkHashes"`
		Indexes           []struct {
			Name string `json:"name"`
		} `json:"indexes"`
	}
	if err := s.process.doJSON(ctx, "POST", "/v1/generations/validate", request, &response, 30*time.Minute); err != nil {
		return GenerationValidation{}, err
	}
	return GenerationValidation{Ready: response.Valid, Files: response.FileCount, Chunks: response.RowCount,
		DuplicateIDs: response.DuplicateChunkIDs, InvalidVectors: response.InvalidVectors, IndexReady: len(response.Indexes) > 0,
		TableVersion:  response.TableVersion,
		ChunkIDDigest: response.ChunkIDDigest, FileIDDigest: response.FileIDDigest,
		FileChunkHashes: response.FileChunkHashes}, nil
}

func (s *LanceRetrievalStore) Stats(ctx context.Context, generationID string) (RetrievalStats, error) {
	if err := s.ensure(ctx); err != nil {
		return RetrievalStats{}, err
	}
	request := struct{ lanceBaseRequest }{lanceBaseRequest{RequestID: lanceRequestID("stats"), AgentKey: s.agentKey(generationID), GenerationID: generationID}}
	var response struct {
		RowCount     int    `json:"rowCount"`
		FileCount    int    `json:"fileCount"`
		TableVersion uint64 `json:"tableVersion"`
		Indexes      []struct {
			Name          string `json:"name"`
			IndexType     string `json:"indexType"`
			UnindexedRows int    `json:"unindexedRows"`
		} `json:"indexes"`
	}
	if err := s.process.doJSON(ctx, "POST", "/v1/stats", request, &response, 30*time.Second); err != nil {
		return RetrievalStats{}, err
	}
	stats := RetrievalStats{Files: response.FileCount, Chunks: response.RowCount, TableVersion: response.TableVersion}
	for _, index := range response.Indexes {
		lower := strings.ToLower(index.IndexType + " " + index.Name)
		if strings.Contains(lower, "fts") {
			stats.FTSIndexType = index.IndexType
			stats.FTSReady = true
		}
		if strings.Contains(lower, "ivf") || strings.Contains(lower, "vector") || strings.Contains(lower, "hnsw") {
			stats.VectorIndexType = index.IndexType
			stats.VectorReady = true
			stats.UnindexedRows += index.UnindexedRows
		}
	}
	if stats.VectorIndexType == "" {
		stats.VectorIndexType = "flat"
		stats.VectorReady = true
	}
	return stats, nil
}

func (s *LanceRetrievalStore) Optimize(ctx context.Context, generationID string, spec OptimizeSpec) error {
	if err := s.ensure(ctx); err != nil {
		return err
	}
	retentionHours := int(spec.VersionRetention / time.Hour)
	request := struct {
		lanceBaseRequest
		RetentionHours int `json:"retentionHours,omitempty"`
	}{lanceBaseRequest: lanceBaseRequest{RequestID: lanceRequestID("optimize"), AgentKey: s.agentKey(generationID), GenerationID: generationID}, RetentionHours: retentionHours}
	return s.process.doJSON(ctx, "POST", "/v1/optimize", request, nil, 30*time.Minute)
}

var _ RetrievalStore = (*LanceRetrievalStore)(nil)

func logLanceRequest(agentKey, generationID, operation, requestID string, started time.Time, detail string, err error) {
	fields := fmt.Sprintf("agentKey=%s generationId=%s operation=%s requestId=%s durationMs=%d",
		agentKey, generationID, operation, requestID, time.Since(started).Milliseconds())
	if strings.TrimSpace(detail) != "" {
		fields += " " + detail
	}
	if err != nil {
		fields += " errorCode=" + lanceErrorCode(err)
	}
	log.Printf("[kbase-lance] %s", fields)
}
