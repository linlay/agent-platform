package kbase

import "agent-platform/internal/catalog"

type RefreshOptions struct {
	Force bool
	Mode  string
}

type RefreshResult struct {
	AgentKey      string `json:"agentKey"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	ScannedFiles  int    `json:"scannedFiles"`
	ChangedFiles  int    `json:"changedFiles"`
	DeletedFiles  int    `json:"deletedFiles"`
	IndexedChunks int    `json:"indexedChunks"`
	Error         string `json:"error,omitempty"`
}

type Status struct {
	AgentKey           string                        `json:"agentKey"`
	Mode               string                        `json:"mode"`
	StorageLocation    string                        `json:"storageLocation"`
	StorageDir         string                        `json:"storageDir"`
	WorkspaceRoot      string                        `json:"workspaceRoot"`
	Indexing           bool                          `json:"indexing"`
	Stale              bool                          `json:"stale"`
	LastIndexedAt      int64                         `json:"lastIndexedAt"`
	Files              int                           `json:"files"`
	Chunks             int                           `json:"chunks"`
	Embedding          EmbeddingSnapshot             `json:"embedding"`
	Chunk              catalog.AgentKBaseChunkConfig `json:"chunk"`
	LastRun            *IndexRun                     `json:"lastRun,omitempty"`
	FileStats          FileStats                     `json:"fileStats,omitempty"`
	ConfigHash         string                        `json:"configHash,omitempty"`
	ManifestConfigHash string                        `json:"manifestConfigHash,omitempty"`
}

type FileStats struct {
	Active     int            `json:"active"`
	Skipped    int            `json:"skipped"`
	Error      int            `json:"error"`
	Deleted    int            `json:"deleted"`
	Extractors map[string]int `json:"extractors,omitempty"`
}

type EmbeddingSnapshot struct {
	ModelKey     string `json:"modelKey,omitempty"`
	ProviderKey  string `json:"providerKey"`
	Model        string `json:"model"`
	Dimension    int    `json:"dimension"`
	Timeout      int    `json:"timeout"`
	EndpointPath string `json:"endpointPath,omitempty"`
}

type SearchOptions struct {
	Limit      int
	Offset     int
	PathPrefix string
	PathGlob   string
	Type       string
}

type SearchResult struct {
	AgentKey   string      `json:"agentKey"`
	Query      string      `json:"query"`
	Count      int         `json:"count"`
	MatchCount int         `json:"matchCount"`
	Offset     int         `json:"offset"`
	Limit      int         `json:"limit"`
	Truncated  bool        `json:"truncated"`
	Results    []SearchHit `json:"results"`
	Stale      bool        `json:"stale,omitempty"`
	Indexing   bool        `json:"indexing,omitempty"`
}

type SearchHit struct {
	ChunkID    string  `json:"chunkId"`
	Path       string  `json:"path"`
	Heading    string  `json:"heading,omitempty"`
	StartLine  int     `json:"startLine"`
	EndLine    int     `json:"endLine"`
	PageStart  int     `json:"pageStart,omitempty"`
	PageEnd    int     `json:"pageEnd,omitempty"`
	SlideStart int     `json:"slideStart,omitempty"`
	SlideEnd   int     `json:"slideEnd,omitempty"`
	SourceType string  `json:"sourceType,omitempty"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
	MatchType  string  `json:"matchType"`
}

type ReadOptions struct {
	ChunkID string
	Path    string
	Offset  int
	Limit   int
}

type ReadResult struct {
	Found      bool   `json:"found"`
	ChunkID    string `json:"chunkId,omitempty"`
	Path       string `json:"path,omitempty"`
	Heading    string `json:"heading,omitempty"`
	StartLine  int    `json:"startLine,omitempty"`
	EndLine    int    `json:"endLine,omitempty"`
	PageStart  int    `json:"pageStart,omitempty"`
	PageEnd    int    `json:"pageEnd,omitempty"`
	SlideStart int    `json:"slideStart,omitempty"`
	SlideEnd   int    `json:"slideEnd,omitempty"`
	SourceType string `json:"sourceType,omitempty"`
	Content    string `json:"content,omitempty"`
}

type FilesOptions struct {
	Mode      string
	Path      string
	Pattern   string
	Status    string
	Type      string
	Depth     int
	HeadLimit int
	Offset    int
}

type FilesResult struct {
	Tool       string      `json:"tool"`
	Mode       string      `json:"mode"`
	Path       string      `json:"path"`
	Pattern    string      `json:"pattern"`
	Status     string      `json:"status"`
	Type       string      `json:"type,omitempty"`
	MatchCount int         `json:"matchCount"`
	FileCount  int         `json:"fileCount"`
	DirCount   int         `json:"dirCount"`
	Truncated  bool        `json:"truncated"`
	Offset     int         `json:"offset"`
	HeadLimit  int         `json:"headLimit"`
	Results    []FileEntry `json:"results"`
}

type FileEntry struct {
	Type       string `json:"type"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Dir        string `json:"dir,omitempty"`
	Depth      int    `json:"depth,omitempty"`
	Ext        string `json:"ext,omitempty"`
	Mime       string `json:"mime,omitempty"`
	Size       int64  `json:"size,omitempty"`
	MTimeMS    int64  `json:"mtimeMs,omitempty"`
	TextSHA256 string `json:"textSha256,omitempty"`
	Extractor  string `json:"extractor,omitempty"`
	Status     string `json:"status,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
	Error      string `json:"error,omitempty"`
	ChunkCount int    `json:"chunkCount,omitempty"`
	FileCount  int    `json:"fileCount,omitempty"`
	IndexedAt  int64  `json:"indexedAt,omitempty"`
}

type IndexRun struct {
	ID            string `json:"id"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	StartedAt     int64  `json:"startedAt"`
	FinishedAt    int64  `json:"finishedAt,omitempty"`
	ScannedFiles  int    `json:"scannedFiles"`
	ChangedFiles  int    `json:"changedFiles"`
	DeletedFiles  int    `json:"deletedFiles"`
	IndexedChunks int    `json:"indexedChunks"`
	Error         string `json:"error,omitempty"`
}

type fileRecord struct {
	ID         string
	Path       string
	Ext        string
	Mime       string
	Size       int64
	MTimeMS    int64
	SHA256     string
	TextSHA256 string
	Extractor  string
	Metadata   string
	Status     string
	SkipReason string
	Error      string
	ChunkCount int
	IndexedAt  int64
}

type chunkRecord struct {
	ID                 string
	FileID             string
	Path               string
	Ordinal            int
	Heading            string
	StartLine          int
	EndLine            int
	PageStart          int
	PageEnd            int
	SlideStart         int
	SlideEnd           int
	SourceType         string
	LocatorJSON        string
	Content            string
	ContentHash        string
	Embedding          []float64
	EmbeddingModel     string
	EmbeddingDimension int
	UpdatedAt          int64
}
