package kbase

import (
	"context"
	"time"
)

const (
	ControlSchemaVersion = "3"

	GenerationBuilding   = "building"
	GenerationIndexing   = "indexing"
	GenerationValidating = "validating"
	GenerationReady      = "ready"
	GenerationActive     = "active"
	GenerationRetired    = "retired"
	GenerationFailed     = "failed"

	FileOperationReplace = "replace"
	FileOperationDelete  = "delete"

	FileOperationPrepared       = "prepared"
	FileOperationLanceCommitted = "lance_committed"
	FileOperationCompleted      = "completed"
)

// MetadataStore owns transactional KBASE control data. Retrieval payloads do
// not live here; implementations must keep generation activation atomic.
type MetadataStore interface {
	Close() error
	Meta(context.Context, string) (string, error)
	SetMeta(context.Context, string, string) error
	BeginRun(context.Context, string, string) (IndexRun, error)
	FinishRun(context.Context, IndexRun, string, string) error
	LastRun(context.Context) (*IndexRun, error)
	File(context.Context, string, string) (*fileRecord, error)
	Files(context.Context, string) ([]fileRecord, error)
	ActiveFilePaths(context.Context, string) (map[string]struct{}, error)
	UpsertFile(context.Context, string, fileRecord) error
	MarkFileDeleted(context.Context, string, string) error
	FileStats(context.Context, string) (FileStats, error)
	CreateGeneration(context.Context, Generation) error
	SetGenerationState(context.Context, string, string, string) error
	Generation(context.Context, string) (*Generation, error)
	ActiveGeneration(context.Context) (*Generation, error)
	ActivateGeneration(context.Context, string) error
	BeginFileOperation(context.Context, FileOperation) error
	MarkFileOperationLanceCommitted(context.Context, string, uint64) error
	CompleteFileOperation(context.Context, string) error
	PendingFileOperations(context.Context, string) ([]FileOperation, error)
}

// RetrievalStore is implemented by the LanceDB sidecar and used by Manager
// and the workspace indexer.
type RetrievalStore interface {
	CreateGeneration(context.Context, GenerationSpec) error
	ImportChunks(context.Context, string, []chunkRecord) error
	ReplaceFileChunks(context.Context, string, string, []chunkRecord) (uint64, error)
	DeleteFileChunks(context.Context, string, string) (uint64, error)
	Search(context.Context, string, RetrievalRequest) (RetrievalResponse, error)
	ReadChunk(context.Context, string, string) (*chunkRecord, error)
	ReadPath(context.Context, string, string, int, int) ([]chunkRecord, error)
	BuildIndexes(context.Context, string, IndexSpec) error
	WaitForIndexes(context.Context, string, time.Duration) error
	Validate(context.Context, string) (GenerationValidation, error)
	Stats(context.Context, string) (RetrievalStats, error)
	Optimize(context.Context, string, OptimizeSpec) error
}

type Generation struct {
	ID                   string `json:"id"`
	AgentKey             string `json:"agentKey"`
	State                string `json:"state"`
	WorkspaceRoot        string `json:"workspaceRoot"`
	StorageDir           string `json:"storageDir"`
	EmbeddingModelKey    string `json:"embeddingModelKey,omitempty"`
	EmbeddingProviderKey string `json:"embeddingProviderKey,omitempty"`
	EmbeddingModel       string `json:"embeddingModel,omitempty"`
	EmbeddingDimension   int    `json:"embeddingDimension"`
	FTSTokenizer         string `json:"ftsTokenizer,omitempty"`
	IndexHash            string `json:"indexHash"`
	TableVersion         uint64 `json:"tableVersion,omitempty"`
	Files                int    `json:"files"`
	Chunks               int    `json:"chunks"`
	CreatedAt            int64  `json:"createdAt"`
	ActivatedAt          int64  `json:"activatedAt,omitempty"`
	RetiredAt            int64  `json:"retiredAt,omitempty"`
	Error                string `json:"error,omitempty"`
}

type FileOperation struct {
	ID                 string `json:"id"`
	GenerationID       string `json:"generationId"`
	FileID             string `json:"fileId"`
	Path               string `json:"path"`
	Operation          string `json:"operation"`
	DesiredContentHash string `json:"desiredContentHash,omitempty"`
	DesiredRecordJSON  string `json:"desiredRecordJson,omitempty"`
	State              string `json:"state"`
	TableVersion       uint64 `json:"tableVersion,omitempty"`
	RetryCount         int    `json:"retryCount"`
	CreatedAt          int64  `json:"createdAt"`
	UpdatedAt          int64  `json:"updatedAt"`
	Error              string `json:"error,omitempty"`
}

type GenerationSpec struct {
	AgentKey           string `json:"agentKey"`
	GenerationID       string `json:"generationId"`
	StorageDir         string `json:"storageDir"`
	EmbeddingModel     string `json:"embeddingModel"`
	EmbeddingDimension int    `json:"embeddingDimension"`
	FTSBaseTokenizer   string `json:"ftsBaseTokenizer,omitempty"`
}

type RetrievalRequest struct {
	Query               string
	Vector              []float32
	Limit               int
	Offset              int
	CandidateFloor      int
	CandidateMultiplier int
	CandidateMax        int
	RRFK                int
	VectorWeight        float64
	FTSWeight           float64
	PathPrefix          string
	PathGlob            string
	Type                string
}

type RetrievalResponse struct {
	Matches    []RetrievalMatch `json:"matches"`
	MatchCount int              `json:"matchCount"`
	Truncated  bool             `json:"truncated"`
	VectorHits int              `json:"vectorHits,omitempty"`
	FTSHits    int              `json:"ftsHits,omitempty"`
}

type RetrievalMatch struct {
	Chunk     chunkRecord
	Score     float64
	MatchType string
}

type IndexSpec struct {
	FTSBaseTokenizer string `json:"ftsBaseTokenizer"`
	ANNMinRows       int    `json:"annMinRows"`
	Distance         string `json:"distance"`
}

type OptimizeSpec struct {
	VersionRetention time.Duration `json:"-"`
}

type GenerationValidation struct {
	Ready           bool              `json:"ready"`
	Files           int               `json:"files"`
	Chunks          int               `json:"chunks"`
	DuplicateIDs    int               `json:"duplicateIds"`
	InvalidVectors  int               `json:"invalidVectors"`
	IndexReady      bool              `json:"indexReady"`
	TableVersion    uint64            `json:"tableVersion,omitempty"`
	ChunkIDDigest   string            `json:"chunkIdDigest,omitempty"`
	FileIDDigest    string            `json:"fileIdDigest,omitempty"`
	FileChunkHashes map[string]string `json:"fileChunkHashes,omitempty"`
	Error           string            `json:"error,omitempty"`
}

type RetrievalStats struct {
	Files           int    `json:"files"`
	Chunks          int    `json:"chunks"`
	TableVersion    uint64 `json:"tableVersion"`
	FTSIndexType    string `json:"ftsIndexType,omitempty"`
	VectorIndexType string `json:"vectorIndexType,omitempty"`
	FTSReady        bool   `json:"ftsReady"`
	VectorReady     bool   `json:"vectorReady"`
	UnindexedRows   int    `json:"unindexedRows"`
	LastOptimizedAt int64  `json:"lastOptimizedAt,omitempty"`
}
