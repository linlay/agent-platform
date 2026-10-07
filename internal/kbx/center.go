package kbx

import (
	"agent-platform/internal/kbasescenter"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CenterEngine implements explicit manual indexing for independent libraries.
// It is separate from the Agent capability's reconnectable-worker contract.
type CenterEngine struct {
	configSource *ModelConfigSource
	runner       Runner
	embedding    bool
}

func NewCenterEngine() *CenterEngine { return &CenterEngine{runner: cliRunner{}} }

var centerConfig = []byte(`{"models":{"embedding":null,"query_expansion":null,"reranker":null,"graph_extraction":null}}`)

func (e *CenterEngine) Update(ctx context.Context, db string, collections []kbasescenter.Collection) error {
	if e.configSource != nil {
		if _, err := e.configSource.Snapshot(); err != nil {
			return err
		}
	}
	if len(collections) == 0 {
		return fmt.Errorf("at least one collection is required")
	}
	// Verify every source before starting any index mutation.
	for _, c := range collections {
		actual, err := filepath.EvalSymlinks(c.SourcePath)
		if err != nil || actual != c.SourcePath {
			return fmt.Errorf("collection %s: source directory is unavailable or changed identity", c.Name)
		}
		st, err := os.Stat(c.SourcePath)
		if err != nil || !st.IsDir() {
			return fmt.Errorf("collection %s: source directory is unavailable", c.Name)
		}
	}
	// Collection registration is checked on every retry: an interrupted initial
	// scan can leave a valid database with an already registered collection.
	registered := map[string]bool{}
	if st, err := os.Lstat(db); err == nil {
		if !st.Mode().IsRegular() {
			return fmt.Errorf("invalid KBX index path")
		}
		raw, err := e.runner.Run(ctx, db, centerConfig, "ls", "--agent")
		if err != nil {
			return err
		}
		var inventory struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		}
		if err = decodeEnvelope(raw, &inventory); err != nil {
			return fmt.Errorf("invalid KBX collection response")
		}
		for _, c := range inventory.Collections {
			registered[c.Name] = true
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	names := make([]string, 0, len(collections))
	for _, c := range collections {
		names = append(names, c.Name)
		args := []string{"collection", "add", c.SourcePath, "--name", c.Name}
		if registered[c.Name] {
			args = []string{"update", "-c", c.Name, "--no-commands"}
		}
		out, err := e.runner.Run(ctx, db, centerConfig, args...)
		if err != nil {
			return fmt.Errorf("collection %s: %w", c.Name, err)
		}
		if !strings.Contains(string(out), "status=complete") || strings.Contains(string(out), "status=partial") {
			return fmt.Errorf("collection %s: KBX indexing was incomplete; inspect the source documents and retry", c.Name)
		}
	}
	if e.embedding {
		for _, c := range collections {
			if _, err := e.runner.Run(ctx, db, centerConfig, "embed", "-c", c.Name); err != nil {
				return fmt.Errorf("collection %s: KBX text index updated, but vector indexing failed: %w", c.Name, err)
			}
		}
		raw, err := e.Read(ctx, db, "status", "", 0, names...)
		if err != nil {
			return err
		}
		var status struct {
			Capabilities struct {
				Vector struct {
					Complete bool `json:"complete"`
				} `json:"vector"`
			} `json:"capabilities"`
		}
		if err = json.Unmarshal(raw, &status); err != nil || !status.Capabilities.Vector.Complete {
			return fmt.Errorf("KBX vector indexing is incomplete")
		}
	}
	return nil
}
func (e *CenterEngine) Read(ctx context.Context, db, operation, arg string, limit int, collections ...string) (json.RawMessage, error) {
	if e.configSource != nil {
		if _, err := e.configSource.Snapshot(); err != nil {
			return nil, err
		}
	}
	var args []string
	switch operation {
	case "status":
		args = []string{"status", "--agent"}
	case "files":
		return e.files(ctx, db, collections)
	case "search", "query", "vsearch", "gsearch":
		args = []string{operation, "--agent", "-n", strconv.Itoa(limit)}
		if operation != "gsearch" {
			args = append(args, "--full")
		}
		if operation == "query" {
			args = append(args, "--no-rerank", "--explain")
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
	raw, err := e.runner.Run(ctx, db, centerConfig, args...)
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

func (e *CenterEngine) files(ctx context.Context, db string, collections []string) (json.RawMessage, error) {
	var documents []json.RawMessage
	complete := true
	for _, name := range collections {
		raw, err := e.runner.Run(ctx, db, centerConfig, "ls", "kbx://"+name, "--agent")
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
		collection, path, ok := kbasescenter.DocumentReference(ref)
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
