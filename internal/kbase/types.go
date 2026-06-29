package kbase

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
	AgentKey           string            `json:"agentKey"`
	Mode               string            `json:"mode"`
	StorageLocation    string            `json:"storageLocation"`
	StorageDir         string            `json:"storageDir"`
	WorkspaceRoot      string            `json:"workspaceRoot"`
	Indexing           bool              `json:"indexing"`
	Stale              bool              `json:"stale"`
	LastIndexedAt      int64             `json:"lastIndexedAt"`
	Files              int               `json:"files"`
	Chunks             int               `json:"chunks"`
	Embedding          EmbeddingSnapshot `json:"embedding"`
	LastRun            *IndexRun         `json:"lastRun,omitempty"`
	FileStats          FileStats         `json:"fileStats,omitempty"`
	ConfigHash         string            `json:"configHash,omitempty"`
	ManifestConfigHash string            `json:"manifestConfigHash,omitempty"`
}

type FileStats struct {
	Active     int            `json:"active"`
	Skipped    int            `json:"skipped"`
	Error      int            `json:"error"`
	Deleted    int            `json:"deleted"`
	Extractors map[string]int `json:"extractors,omitempty"`
}

type EmbeddingSnapshot struct {
	ProviderKey string `json:"providerKey"`
	Model       string `json:"model"`
	Dimension   int    `json:"dimension"`
	Timeout     int    `json:"timeout"`
}

type SearchOptions struct {
	Limit int
}

type SearchResult struct {
	AgentKey string      `json:"agentKey"`
	Query    string      `json:"query"`
	Count    int         `json:"count"`
	Results  []SearchHit `json:"results"`
	Stale    bool        `json:"stale,omitempty"`
	Indexing bool        `json:"indexing,omitempty"`
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
