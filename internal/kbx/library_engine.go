package kbx

import (
	"agent-platform/internal/kbases"
	"agent-platform/internal/knowledge"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LibraryEngine maintains shared libraries through the structured KBX protocol.
type LibraryEngine struct {
	configSource   *ModelConfigSource
	runner         Runner
	embedding      bool
	readConfig     []byte
	searchDefaults knowledge.SearchOptions
	candidateLimit int
}

func NewLibraryEngine() *LibraryEngine { return &LibraryEngine{runner: cliRunner{}} }

var defaultLibraryConfig = []byte(`{"models":{"embedding":null,"query_expansion":null,"reranker":null,"graph_extraction":null}}`)

func (e *LibraryEngine) Update(ctx context.Context, db string, collections []kbases.Collection) error {
	return e.UpdatePaths(ctx, db, collections, nil)
}

// UpdatePaths uses the same structured maintenance contract as the former Agent
// worker, now scoped to one shared database and explicit collections.
func (e *LibraryEngine) UpdatePaths(ctx context.Context, db string, collections []kbases.Collection, changes map[string][]string) error {
	return e.UpdateLibrary(ctx, db, kbases.Definition{Collections: collections}, changes)
}
func (e *LibraryEngine) UpdateLibrary(ctx context.Context, db string, definition kbases.Definition, changes map[string][]string) error {
	collections := definition.Collections
	if len(collections) == 0 {
		return fmt.Errorf("at least one collection is required")
	}
	cfg, err := e.libraryConfig(definition, true)
	if err != nil {
		return fmt.Errorf("%w: %w", kbases.ErrNotStarted, err)
	}
	m := NewManager(Options{}, nil, nil)
	m.runner = e.runner
	m.frozenConfig = cfg
	m.skipEmbedding = true
	if err := m.probeMaintenance(ctx); err != nil {
		return fmt.Errorf("%w: %w", kbases.ErrNotStarted, err)
	}
	for _, c := range collections {
		actual, err := filepath.EvalSymlinks(c.SourcePath)
		if err != nil || actual != c.SourcePath {
			return fmt.Errorf("%w: collection %s source unavailable or changed", kbases.ErrNotStarted, c.Name)
		}
		st, err := os.Stat(c.SourcePath)
		if err != nil || !st.IsDir() {
			return fmt.Errorf("%w: source must be a directory", kbases.ErrNotStarted)
		}
		for _, p := range append(append([]string{}, c.Include...), c.Exclude...) {
			if err := knowledge.ValidateSourcePattern(p); err != nil {
				return fmt.Errorf("%w: %w", kbases.ErrNotStarted, err)
			}
		}
	}
	// Reconcile removals/path changes before registering any collections. The
	// service has already withdrawn old-scope reads when the fingerprint changed.
	if _, err := os.Stat(db); err == nil {
		l := library{definition: definition, database: db, spec: knowledge.AgentSpec{Config: knowledge.DefaultConfig()}}
		cfg, err := m.config(l, true)
		if err != nil {
			return fmt.Errorf("%w: %w", kbases.ErrNotStarted, err)
		}
		response, err := m.retryMaintenance(ctx, l, cfg, "collection.list", "collection", "list")
		if err != nil {
			return fmt.Errorf("%w: %w", kbases.ErrNotStarted, err)
		}
		var old struct{ Collections []struct{ Name, Path string } }
		if err = json.Unmarshal(response.Data, &old); err != nil {
			return fmt.Errorf("%w: %w", kbases.ErrNotStarted, err)
		}
		wanted := map[string]string{}
		for _, c := range collections {
			wanted[c.Name] = c.SourcePath
		}
		for _, c := range old.Collections {
			if wanted[c.Name] != c.Path {
				if _, err = e.runner.Run(ctx, db, cfg, "collection", "remove", c.Name); err != nil {
					return err
				}
				changes = nil
			}
		}
	}
	var degraded error
	for _, c := range collections {
		if c.Include == nil {
			c.Include = knowledge.DefaultIncludePatterns()
		}
		if c.Exclude == nil {
			c.Exclude = knowledge.DefaultExcludePatterns()
		}
		defaults := knowledge.ChunkSettings{}
		if definition.Chunk != nil {
			defaults = *definition.Chunk
		}
		chunk, err := knowledge.ResolveSourceChunk(defaults, c.Chunk)
		if err != nil {
			return err
		}
		c.Chunk = knowledge.ChunkSettingsFrom(chunk)
		l := library{definition: definition, database: db, collection: c.Name, source: c, spec: knowledge.AgentSpec{Key: c.Name, WorkspaceRoot: c.SourcePath, Config: knowledge.DefaultConfig()}}
		w := &collectionUpdate{library: l}
		job := &updatePaths{}
		if changes != nil {
			if paths, ok := changes[c.Name]; ok {
				job.paths = paths
				job.incremental = true
			} else {
				continue
			}
		}
		err = m.performRefresh(ctx, w, job)
		if err != nil {
			if !w.initialized || w.index == nil || !w.index.FullText.Ready {
				return err
			}
			degraded = errors.Join(degraded, fmt.Errorf("collection %s: %w", c.Name, err))
		}
	}
	if degraded != nil {
		return &kbases.ReadableFailure{Err: degraded}
	}
	if err := e.embedLibrary(ctx, db, cfg, definition.VectorsPending); err != nil {
		return &kbases.ReadableFailure{Err: err}
	}
	return nil
}

func (e *LibraryEngine) Read(ctx context.Context, db, operation, arg string, limit int, collections ...string) (json.RawMessage, error) {
	return e.ReadLibrary(ctx, db, kbases.Definition{}, operation, arg, limit, collections...)
}
func (e *LibraryEngine) ReadLibrary(ctx context.Context, db string, definition kbases.Definition, operation, arg string, limit int, collections ...string) (json.RawMessage, error) {
	embedding := operation == "query" || operation == "vsearch" || operation == "status"
	if definition.VectorsPending {
		if operation == "vsearch" {
			return nil, unavailable("knowledge vectors are rebuilding for the current embedding configuration")
		}
		embedding = false
	}
	cfg, err := e.libraryConfig(definition, embedding)
	if err != nil {
		return nil, err
	}
	local := *e
	retrieval, err := knowledge.MergeRetrieval(definition.Retrieval, knowledge.DefaultConfig())
	if err != nil {
		return nil, err
	}
	o := retrieval.ApplySearchDefaults(knowledge.SearchOptions{Method: operation, Limit: limit})
	m := NewManager(Options{ConfigSource: e.configSource}, nil, nil)
	cfg, err = m.queryConfig(cfg, definition, operation, &o)
	if err != nil {
		return nil, err
	}
	local.readConfig = cfg
	local.searchDefaults = o
	local.candidateLimit = min(retrieval.CandidateMax, max(retrieval.CandidateFloor, o.Limit*retrieval.CandidateMultiplier))
	limit = o.Limit
	return local.read(ctx, db, operation, arg, limit, collections...)
}
func (e *LibraryEngine) read(ctx context.Context, db, operation, arg string, limit int, collections ...string) (json.RawMessage, error) {
	var args []string
	switch operation {
	case "status":
		args = []string{"status", "--agent"}
	case "files":
		return e.files(ctx, db, collections)
	case "search", "query", "vsearch", "gsearch":
		args = []string{operation, "--agent", "-n", strconv.Itoa(limit)}
		if operation != "gsearch" {
			args = append(args, "--full", "-C", strconv.Itoa(max(limit, e.candidateLimit)))
			for _, field := range []struct {
				flag  string
				value *float64
			}{{"--min-score", e.searchDefaults.MinScore}, {"--recency-weight", e.searchDefaults.RecencyWeight}, {"--recency-half-life-days", e.searchDefaults.RecencyHalfLifeDays}} {
				if field.value != nil {
					args = append(args, field.flag, strconv.FormatFloat(*field.value, 'g', -1, 64))
				}
			}
		}
		if operation == "query" {
			args = append(args, "--explain")
			if e.searchDefaults.Rerank != nil && !*e.searchDefaults.Rerank {
				args = append(args, "--no-rerank")
			}
		}
		for _, name := range collections {
			args = append(args, "-c", name)
		}
		args = append(args, "--", arg)
	case "read":
		args = []string{"get", "--agent", "--no-line-numbers", "--lines", "200", "--", arg}
	default:
		return nil, fmt.Errorf("unknown KBX operation")
	}
	raw, err := e.runner.Run(ctx, db, e.readConfig, args...)
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if err = decodeEnvelope(raw, &result); err != nil {
		return nil, err
	}
	if operation == "search" || operation == "query" || operation == "vsearch" || operation == "gsearch" {
		// Graph document retrieval emits an array rather than the traced search object.
		if operation == "gsearch" && len(result) > 0 && strings.HasPrefix(strings.TrimSpace(string(result)), "[") {
			result, err = json.Marshal(map[string]any{"results": result})
			if err != nil {
				return nil, err
			}
		}
		result, err = withSimilarityScores(result, operation)
		if err != nil {
			return nil, err
		}
		return withDocumentSources(result, "results", collections)
	}
	return result, nil
}

func (e *LibraryEngine) files(ctx context.Context, db string, collections []string) (json.RawMessage, error) {
	var documents []json.RawMessage
	complete := true
	for _, name := range collections {
		raw, err := e.runner.Run(ctx, db, e.readConfig, "ls", "kbx://"+name, "--agent")
		if err != nil {
			return nil, err
		}
		var inventory struct {
			Documents []json.RawMessage `json:"documents"`
			Complete  bool              `json:"complete"`
		}
		if err := decodeEnvelope(raw, &inventory); err != nil {
			return nil, err
		}
		documents = append(documents, inventory.Documents...)
		complete = complete && inventory.Complete
	}
	if documents == nil {
		documents = []json.RawMessage{}
	}
	result, err := json.Marshal(map[string]any{"documents": documents, "complete": complete})
	if err != nil {
		return nil, err
	}
	return withDocumentSources(result, "documents", collections)
}

// Expose source fields while preserving KBX's chunk, evidence and ranking data.
func withDocumentSources(raw json.RawMessage, key string, allowed []string) (json.RawMessage, error) {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(data[key], &rows); err != nil {
		return nil, fmt.Errorf("invalid KBX document response")
	}
	names := map[string]bool{}
	for _, name := range allowed {
		names[name] = true
	}
	for _, row := range rows {
		var ref string
		if err := json.Unmarshal(row["file"], &ref); err != nil {
			return nil, fmt.Errorf("invalid KBX document reference")
		}
		collection, path, ok := kbases.DocumentReference(ref)
		if !ok || (len(allowed) > 0 && !names[collection]) {
			return nil, fmt.Errorf("KBX returned a document outside the selected collections")
		}
		row["collection"], _ = json.Marshal(collection)
		row["relativePath"], _ = json.Marshal(path)
	}
	if rows == nil {
		rows = []map[string]json.RawMessage{}
	}
	data[key], _ = json.Marshal(rows)
	return json.Marshal(data)
}
