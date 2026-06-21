package memory

import (
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/api"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	root            string
	dbPath          string
	dualWriteMD     bool
	mu              sync.Mutex
	db              *sql.DB
	ftsVectorWeight float64
	ftsFTSWeight    float64
	embedder        *EmbeddingProvider
	summarizer      RememberSummarizer
	runtimeResolver RuntimeResolver
}

func NewSQLiteStore(root string, dbFileName string) (*SQLiteStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	if strings.TrimSpace(dbFileName) == "" {
		dbFileName = "memory.db"
	}
	store := &SQLiteStore{
		root:            root,
		dbPath:          filepath.Join(root, dbFileName),
		dualWriteMD:     true,
		ftsVectorWeight: 0.7,
		ftsFTSWeight:    0.3,
	}
	if err := store.initDB(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) SetEmbedder(ep *EmbeddingProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.embedder = ep
}

func (s *SQLiteStore) SetRememberSummarizer(summarizer RememberSummarizer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summarizer = summarizer
}

func (s *SQLiteStore) SetRuntimeResolver(resolver RuntimeResolver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtimeResolver = resolver
}

func (s *SQLiteStore) runtimeForAgent(agentKey string) RuntimeConfig {
	if s == nil {
		return RuntimeConfig{}
	}
	s.mu.Lock()
	resolver := s.runtimeResolver
	embedder := s.embedder
	summarizer := s.summarizer
	s.mu.Unlock()
	runtime := RuntimeConfig{
		Embedder:   embedder,
		Summarizer: summarizer,
	}
	if resolver == nil {
		return runtime
	}
	resolved := resolver(agentKey)
	if resolved.Embedder != nil {
		runtime.Embedder = resolved.Embedder
	}
	if resolved.Summarizer != nil {
		runtime.Summarizer = resolved.Summarizer
	}
	return runtime
}

func (s *SQLiteStore) Search(query string, limit int) ([]api.StoredMemoryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 10
	}
	needle := strings.TrimSpace(query)
	if needle == "" {
		return s.listRecent(limit)
	}

	// FTS5 search
	ftsResults, err := s.ftsSearch(needle, limit*3)
	if err != nil {
		// Fallback to LIKE search on FTS failure
		ftsResults, _ = s.likeSearch(needle, limit*3)
	}

	// Score normalization and sorting
	if len(ftsResults) == 0 {
		logMemoryOperation("search", map[string]any{"query": query, "limit": limit, "count": 0})
		return []api.StoredMemoryResponse{}, nil
	}

	// Normalize FTS scores
	normalizeScores(ftsResults)

	// Sort by score desc, importance desc, updatedAt desc
	sort.SliceStable(ftsResults, func(i, j int) bool {
		if ftsResults[i].score != ftsResults[j].score {
			return ftsResults[i].score > ftsResults[j].score
		}
		if ftsResults[i].item.Importance != ftsResults[j].item.Importance {
			return ftsResults[i].item.Importance > ftsResults[j].item.Importance
		}
		return ftsResults[i].item.UpdatedAt > ftsResults[j].item.UpdatedAt
	})

	out := make([]api.StoredMemoryResponse, 0, limit)
	for i, r := range ftsResults {
		if i >= limit {
			break
		}
		// Update access tracking
		now := time.Now().UnixMilli()
		_, _ = s.db.Exec(
			`UPDATE MEMORIES SET ACCESS_COUNT_ = ACCESS_COUNT_ + 1, LAST_ACCESSED_AT_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
			now, now, r.item.ID,
		)
		out = append(out, r.item)
	}
	logMemoryOperation("search", map[string]any{"query": query, "limit": limit, "count": len(out)})
	return out, nil
}

func (s *SQLiteStore) SearchDetailed(agentKey string, query string, category string, limit int) ([]ScoredRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit = normalizeLimit(limit, 10)
	needle := strings.TrimSpace(query)
	if needle == "" {
		return []ScoredRecord{}, nil
	}
	normalizedCategory := normalizeOptionalCategory(category)
	results, err := s.ftsSearchDetailed(strings.TrimSpace(agentKey), normalizedCategory, needle, limit*3)
	if err != nil {
		results, err = s.likeSearchDetailed(strings.TrimSpace(agentKey), normalizedCategory, needle, limit*3)
		if err != nil {
			return nil, err
		}
	}
	if len(results) == 0 {
		return []ScoredRecord{}, nil
	}
	normalizeDetailedScores(results)
	out := make([]ScoredRecord, 0, min(len(results), limit))
	for i, result := range results {
		if i >= limit {
			break
		}
		now := time.Now().UnixMilli()
		_, _ = s.db.Exec(
			`UPDATE MEMORIES SET ACCESS_COUNT_ = ACCESS_COUNT_ + 1, LAST_ACCESSED_AT_ = ?, UPDATED_AT_ = ? WHERE ID_ = ?`,
			now, now, result.memory.ID,
		)
		result.memory.AccessCount++
		result.memory.LastAccessedAt = &now
		result.memory.UpdatedAt = now
		out = append(out, ScoredRecord{
			Memory:    result.memory,
			Score:     result.score,
			MatchType: result.matchType,
		})
	}
	sortScoredRecords(out)
	logMemoryOperation("search_detailed", map[string]any{"agentKey": agentKey, "query": query, "category": category, "limit": limit, "count": len(out)})
	return out, nil
}

func (s *SQLiteStore) Write(item api.StoredMemoryResponse) error {
	embedder := s.runtimeForAgent(item.AgentKey).Embedder
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.writeLocked(item, embedder)
	if err == nil {
		logMemoryWrite("write", normalizeStoredItem(item))
	}
	return err
}
