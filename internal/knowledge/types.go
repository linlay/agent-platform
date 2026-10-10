package knowledge

import "encoding/json"

type Status struct {
	LibraryID        string            `json:"libraryId,omitempty"`
	State            string            `json:"state,omitempty"`
	ChunksKnown      *bool             `json:"chunksKnown,omitempty"`
	AgentKey         string            `json:"agentKey"`
	Mode             string            `json:"mode"`
	StorageLocation  string            `json:"storageLocation"`
	StorageDir       string            `json:"storageDir"`
	WorkspaceRoot    string            `json:"workspaceRoot"`
	Indexing         bool              `json:"indexing"`
	Stale            bool              `json:"stale"`
	Degraded         bool              `json:"degraded,omitempty"`
	Error            string            `json:"error,omitempty"`
	LastIndexedAt    *int64            `json:"lastIndexedAt,omitempty"`
	Files            int               `json:"files"`
	Chunks           int               `json:"chunks"`
	Embedding        EmbeddingSnapshot `json:"embedding"`
	Chunk            ChunkConfig       `json:"chunk"`
	LastRun          *IndexRun         `json:"lastRun,omitempty"`
	FileStats        FileStats         `json:"fileStats,omitempty"`
	Engine           string            `json:"engine,omitempty"`
	Indexes          *IndexesStatus    `json:"indexes,omitempty"`
	Sidecar          *RuntimeState     `json:"sidecar,omitempty"`
	PendingChanges   int               `json:"pendingChanges,omitempty"`
	StorageDiskUsage int64             `json:"storageDiskUsage,omitempty"`
}

type IndexesStatus struct {
	FTS    IndexStatus       `json:"fts"`
	Vector VectorIndexStatus `json:"vector"`
}

type IndexStatus struct {
	Type  string `json:"type"`
	Ready bool   `json:"ready"`
}

type VectorIndexStatus struct {
	Type                string `json:"type"`
	Ready               bool   `json:"ready"`
	PendingContentUnits *int   `json:"pendingContentUnits,omitempty"`
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
	Collections         []string `json:"collections"`
	Rerank              *bool    `json:"rerank"`
	QueryExpansion      *bool    `json:"queryExpansion"`
	Method              string   `json:"method"`
	Limit               int      `json:"limit"`
	Offset              int      `json:"offset"`
	PathPrefix          string   `json:"pathPrefix"`
	PathGlob            string   `json:"pathGlob"`
	Type                string   `json:"type"`
	Filter              string   `json:"filter"`
	Exclude             []string `json:"exclude"`
	Intent              string   `json:"intent"`
	MinScore            *float64 `json:"minScore"`
	CandidateLimit      int      `json:"candidateLimit"`
	RecencyWeight       *float64 `json:"recencyWeight"`
	RecencyHalfLifeDays *float64 `json:"recencyHalfLifeDays"`
	NoGraph             bool     `json:"noGraph"`
	Entities            []string `json:"entities"`
	Relations           []string `json:"relations"`
	Direction           string   `json:"direction"`
	MaxHops             int      `json:"maxHops"`
}

type SearchResult struct {
	LibraryID                string      `json:"libraryId,omitempty"`
	Method                   string      `json:"method,omitempty"`
	RetrievalChannels        []string    `json:"retrievalChannels,omitempty"`
	OptionalUnavailable      []string    `json:"optionalUnavailable,omitempty"`
	Engine                   string      `json:"engine,omitempty"`
	Degraded                 bool        `json:"degraded,omitempty"`
	CandidateBudgetExhausted bool        `json:"candidateBudgetExhausted,omitempty"`
	AgentKey                 string      `json:"agentKey"`
	Query                    string      `json:"query"`
	Count                    int         `json:"count"`
	MatchCount               int         `json:"matchCount"`
	Offset                   int         `json:"offset"`
	Limit                    int         `json:"limit"`
	Truncated                bool        `json:"truncated"`
	Results                  []SearchHit `json:"results"`
	Stale                    bool        `json:"stale,omitempty"`
	Indexing                 bool        `json:"indexing,omitempty"`
}

type SearchHit struct {
	LibraryID  string            `json:"libraryId,omitempty"`
	Graph      *GraphExplanation `json:"graph,omitempty"`
	ResultID   string            `json:"resultId,omitempty"`
	EvidenceID string            `json:"evidenceId,omitempty"`
	ChunkID    string            `json:"chunkId"`
	Path       string            `json:"path"`
	Heading    string            `json:"heading,omitempty"`
	StartLine  int               `json:"startLine"`
	EndLine    int               `json:"endLine"`
	PageStart  int               `json:"pageStart,omitempty"`
	PageEnd    int               `json:"pageEnd,omitempty"`
	SlideStart int               `json:"slideStart,omitempty"`
	SlideEnd   int               `json:"slideEnd,omitempty"`
	SourceType string            `json:"sourceType,omitempty"`
	Snippet    string            `json:"snippet"`
	Score      float64           `json:"score"`
	MatchType  string            `json:"matchType"`
}

// Graph explanations retain the relationship path and independently readable
// evidence. Scores are KBX ranking values, not confidence in an answer.
type GraphExplanation struct {
	Links           []GraphLink        `json:"links"`
	BestPath        *GraphPath         `json:"bestPath,omitempty"`
	SupportingPaths int                `json:"supportingPaths"`
	Score           map[string]float64 `json:"score"`
}

type GraphLink struct {
	Mention   string  `json:"mention"`
	EntityKey string  `json:"entityKey"`
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	Score     float64 `json:"score"`
	Source    string  `json:"source"`
}

type GraphPath struct {
	Score float64     `json:"score"`
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

type GraphNode struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	Name string `json:"name"`
}

type GraphEdge struct {
	Predicate  string          `json:"predicate"`
	Confidence float64         `json:"confidence"`
	Evidence   []GraphEvidence `json:"evidence"`
}

type GraphEvidence struct {
	Path       string `json:"path"`
	EvidenceID string `json:"evidenceId"`
	StartLine  int    `json:"startLine"`
	EndLine    int    `json:"endLine"`
	Content    string `json:"content"`
}

type ReadOptions struct {
	ChunkID string
	Path    string
	Offset  int
	Limit   int
}

type ReadResult struct {
	LibraryID    string `json:"libraryId,omitempty"`
	HasMore      bool   `json:"hasMore,omitempty"`
	NextEvidence string `json:"nextEvidence,omitempty"`
	Found        bool   `json:"found"`
	ChunkID      string `json:"chunkId,omitempty"`
	Path         string `json:"path,omitempty"`
	Heading      string `json:"heading,omitempty"`
	StartLine    int    `json:"startLine,omitempty"`
	EndLine      int    `json:"endLine,omitempty"`
	PageStart    int    `json:"pageStart,omitempty"`
	PageEnd      int    `json:"pageEnd,omitempty"`
	SlideStart   int    `json:"slideStart,omitempty"`
	SlideEnd     int    `json:"slideEnd,omitempty"`
	SourceType   string `json:"sourceType,omitempty"`
	Content      string `json:"content,omitempty"`
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
	LibraryID  string      `json:"libraryId,omitempty"`
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
	MTimeMS    *int64 `json:"mtimeMs,omitempty"`
	TextSHA256 string `json:"textSha256,omitempty"`
	Extractor  string `json:"extractor,omitempty"`
	Status     string `json:"status,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
	Error      string `json:"error,omitempty"`
	ChunkCount int    `json:"chunkCount,omitempty"`
	FileCount  int    `json:"fileCount,omitempty"`
	IndexedAt  *int64 `json:"indexedAt,omitempty"`
}

type IndexRun struct {
	ID                   string `json:"id"`
	Engine               string `json:"engine,omitempty"`
	Mode                 string `json:"mode"`
	Scope                string `json:"scope,omitempty"`
	Status               string `json:"status"`
	StartedAt            int64  `json:"startedAt"`
	FinishedAt           int64  `json:"finishedAt,omitempty"`
	ScannedFiles         int    `json:"scannedFiles"`
	CandidatePaths       int    `json:"candidatePaths,omitempty"`
	ChangedFiles         int    `json:"changedFiles"`
	NewFiles             int    `json:"newFiles,omitempty"`
	ModifiedFiles        int    `json:"modifiedFiles,omitempty"`
	MetadataOnlyFiles    int    `json:"metadataOnlyFiles,omitempty"`
	UnchangedFiles       int    `json:"unchangedFiles,omitempty"`
	DeletedFiles         int    `json:"deletedFiles"`
	IndexedChunks        int    `json:"indexedChunks"`
	EmbeddedChunks       int    `json:"embeddedChunks,omitempty"`
	ReusedChunks         int    `json:"reusedChunks,omitempty"`
	PendingChanges       int    `json:"pendingChanges,omitempty"`
	IndexBuildDurationMS int64  `json:"indexBuildDurationMs,omitempty"`
	ValidationDurationMS int64  `json:"validationDurationMs,omitempty"`
	Error                string `json:"error,omitempty"`
}

// KBX status has no exact chunk count in the current reader protocol.
func (s Status) MarshalJSON() ([]byte, error) {
	type plain Status
	b, err := json.Marshal(plain(s))
	if err != nil || s.Engine != "kbx" {
		return b, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	delete(fields, "chunks")
	fields["chunksKnown"] = json.RawMessage("false")
	return json.Marshal(fields)
}
