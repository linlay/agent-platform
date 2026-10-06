package kbx

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"agent-platform/internal/kbase"
)

type textRange struct {
	ByteStart int `json:"byteStart"`
	ByteEnd   int `json:"byteEnd"`
	LineStart int `json:"lineStart"`
	LineEnd   int `json:"lineEnd"`
}
type evidence struct {
	ID          string    `json:"id"`
	ContentHash string    `json:"contentHash"`
	Range       textRange `json:"range"`
	Text        string    `json:"text"`
}
type searchResponse struct {
	Type             string `json:"type"`
	RetrievalVersion int    `json:"retrievalVersion"`
	Results          []struct {
		File     string
		Title    string
		Score    float64
		ResultID string `json:"resultId"`
		Chunk    struct {
			ID    string
			Range textRange
		}
		Evidence evidence
	}
	Trace struct {
		Steps []struct {
			Reason string `json:"reason"`
		} `json:"steps"`
		Coverage struct {
			RetrievalUsed       []string `json:"retrievalUsed"`
			OptionalUnavailable []string `json:"optionalUnavailable"`
		} `json:"coverage"`
		ResultUnit               string `json:"resultUnit"`
		Degraded                 bool
		CandidateBudgetExhausted bool `json:"candidateBudgetExhausted"`
	} `json:"trace"`
}

func predicate(op, value string) map[string]any {
	return map[string]any{"op": op, "key": "sys.path", "value": value}
}
func appendFilter(args []string, v any) []string {
	b, _ := json.Marshal(v)
	return append(args, "--filter", string(b))
}
func (m *Manager) Search(ctx context.Context, key, query string, o kbase.SearchOptions) (kbase.SearchResult, error) {
	l, err := m.resolve(key)
	if err != nil {
		return kbase.SearchResult{}, err
	}
	if strings.TrimSpace(query) == "" {
		return kbase.SearchResult{}, fmt.Errorf("query must not be blank")
	}
	if o.Offset != 0 {
		return kbase.SearchResult{}, fmt.Errorf("KBX chunk search does not support offset")
	}
	limit := o.Limit
	if limit <= 0 {
		limit = l.spec.Config.Retrieval.TopK
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 50 {
		return kbase.SearchResult{}, fmt.Errorf("limit must be at most 50")
	}
	candidate := l.spec.Config.Retrieval.CandidateFloor
	multiplier := l.spec.Config.Retrieval.CandidateMultiplier
	if multiplier < 1 {
		multiplier = 4
	}
	if candidate < limit*multiplier {
		candidate = limit * multiplier
	}
	if ceiling := l.spec.Config.Retrieval.CandidateMax; ceiling > 0 && candidate > ceiling {
		candidate = ceiling
	}
	if candidate < limit {
		candidate = limit
	}
	if candidate > 2000 {
		candidate = 2000
	}
	args := []string{"query", "--agent", "--no-graph", "--no-rerank", "--full", "-c", "workspace", "-n", strconv.Itoa(limit), "-C", strconv.Itoa(candidate)}
	for _, f := range []struct{ op, value string }{{"pathPrefix", o.PathPrefix}, {"pathGlob", o.PathGlob}, {"extension", o.Type}} {
		if f.value != "" {
			args = appendFilter(args, predicate(f.op, f.value))
		}
	}
	if len(l.spec.Config.Include) > 0 {
		items := []any{}
		for _, p := range l.spec.Config.Include {
			items = append(items, predicate("pathGlob", p))
		}
		args = appendFilter(args, map[string]any{"op": "or", "args": items})
	}
	for _, p := range append(append([]string{}, l.spec.Config.Exclude...), ".kbx-platform/**") {
		args = appendFilter(args, map[string]any{"op": "not", "arg": predicate("pathGlob", p)})
	}
	args = append(args, "--", query)
	var response searchResponse
	if err = m.call(ctx, l, true, &response, args...); err != nil {
		return kbase.SearchResult{}, err
	}
	for _, step := range response.Trace.Steps {
		if step.Reason == "vector_index_unavailable" || step.Reason == "dimension_mismatch" {
			return kbase.SearchResult{}, unavailable("KBX vector index is unavailable or incompatible with runtime.kbx.embedding; inspect KBX status and explicitly rebuild vectors if required")
		}
	}
	if response.Type != "kbx.search.response" || response.RetrievalVersion != 6 || response.Trace.ResultUnit != "chunk" {
		return kbase.SearchResult{}, unavailable("KBX retrieval contract 6 with chunk results is required")
	}
	result := kbase.SearchResult{AgentKey: key, Query: query, Limit: limit, Results: []kbase.SearchHit{}, Engine: "kbx", Stale: true, Degraded: response.Trace.Degraded, CandidateBudgetExhausted: response.Trace.CandidateBudgetExhausted}
	for _, hit := range response.Results {
		p, err := documentPath(hit.File)
		if err != nil {
			return kbase.SearchResult{}, err
		}
		if hit.Chunk.ID == "" || hit.Evidence.ID == "" {
			return kbase.SearchResult{}, unavailable("KBX result has no chunk/evidence locator")
		}
		result.Results = append(result.Results, kbase.SearchHit{ChunkID: hit.Chunk.ID, ResultID: hit.ResultID, EvidenceID: hit.Evidence.ID, Path: p, Heading: hit.Title, StartLine: hit.Chunk.Range.LineStart, EndLine: hit.Chunk.Range.LineEnd, SourceType: strings.TrimPrefix(strings.ToLower(path.Ext(p)), "."), Snippet: hit.Evidence.Text, Score: hit.Score, MatchType: "kbx"})
	}
	result.RetrievalChannels = response.Trace.Coverage.RetrievalUsed
	result.OptionalUnavailable = response.Trace.Coverage.OptionalUnavailable
	result.Count = len(result.Results)
	return result, nil
}
func documentPath(uri string) (string, error) {
	const prefix = "kbx://workspace/"
	if !strings.HasPrefix(uri, prefix) {
		return "", unavailable("KBX returned a document outside the workspace collection")
	}
	return relativePath(strings.TrimPrefix(uri, prefix))
}
func relativePath(p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") || strings.Contains(p, ":") {
		return "", fmt.Errorf("path must be workspace-relative")
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." {
			return "", fmt.Errorf("path must not traverse parents")
		}
	}
	p = path.Clean(p)
	if p == "." {
		return "", fmt.Errorf("document path required")
	}
	return p, nil
}

var evidencePattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}#bytes=[0-9]+-[0-9]+$`)

func (m *Manager) Read(key string, o kbase.ReadOptions) (kbase.ReadResult, error) {
	l, err := m.resolve(key)
	if err != nil {
		return kbase.ReadResult{}, err
	}
	ctx, cancel := readerContext()
	defer cancel()
	args := []string{"get", "--agent", "--no-line-numbers"}
	if o.ChunkID != "" {
		if !evidencePattern.MatchString(o.ChunkID) {
			return kbase.ReadResult{}, fmt.Errorf("chunkId must be a KBX content-addressed locator")
		}
		if o.Offset != 0 || o.Limit != 0 {
			return kbase.ReadResult{}, fmt.Errorf("chunkId returns its exact range; use path for line pagination")
		}
		args = append(args, "--evidence", o.ChunkID)
	} else {
		p, e := relativePath(o.Path)
		if e != nil {
			return kbase.ReadResult{}, e
		}
		n := o.Limit
		if n <= 0 {
			n = 80
		}
		if n > 2000 {
			n = 2000
		}
		if o.Offset < 0 {
			return kbase.ReadResult{}, fmt.Errorf("offset must not be negative")
		}
		args = append(args, "--from", strconv.Itoa(max(1, o.Offset)), "--lines", strconv.Itoa(n), "--", "kbx://workspace/"+p)
	}
	var r struct {
		File, Title, Body string
		Evidence          evidence
		ReadRange         struct {
			From, Through int
			HasMore       bool   `json:"hasMore"`
			NextEvidence  string `json:"nextEvidence"`
		} `json:"readRange"`
	}
	if err = m.call(ctx, l, false, &r, args...); err != nil {
		return kbase.ReadResult{}, err
	}
	p, err := documentPath(r.File)
	if err != nil {
		return kbase.ReadResult{}, err
	}
	if !kbase.IndexedPathAllowed(p, l.spec.Config.Include, append(append([]string{}, l.spec.Config.Exclude...), ".kbx-platform/**")) {
		return kbase.ReadResult{}, fmt.Errorf("document is excluded by the knowledge-base policy")
	}
	content := r.Evidence.Text
	if content == "" {
		content = r.Body
	}
	return kbase.ReadResult{Found: true, ChunkID: o.ChunkID, Path: p, Heading: r.Title, StartLine: r.ReadRange.From, EndLine: r.ReadRange.Through, Content: content, HasMore: r.ReadRange.HasMore, NextEvidence: r.ReadRange.NextEvidence}, nil
}
func (m *Manager) Files(key string, o kbase.FilesOptions) (kbase.FilesResult, error) {
	l, err := m.resolve(key)
	if err != nil {
		return kbase.FilesResult{}, err
	}
	if o.Status != "" && o.Status != "active" {
		return kbase.FilesResult{}, fmt.Errorf("KBX exposes active indexed files only")
	}
	ctx, cancel := readerContext()
	defer cancel()
	var r struct {
		Complete  bool
		Documents []struct{ File string }
	}
	if err = m.call(ctx, l, false, &r, "ls", "kbx://workspace", "--agent"); err != nil {
		return kbase.FilesResult{}, err
	}
	if !r.Complete {
		return kbase.FilesResult{}, unavailable("KBX file inventory is incomplete")
	}
	entries := []kbase.FileEntry{}
	for _, d := range r.Documents {
		p, e := documentPath(d.File)
		if e != nil {
			return kbase.FilesResult{}, e
		}
		if kbase.IndexedPathAllowed(p, l.spec.Config.Include, append(append([]string{}, l.spec.Config.Exclude...), ".kbx-platform/**")) {
			entries = append(entries, kbase.FileEntry{Path: p, Ext: strings.ToLower(path.Ext(p)), Status: "active"})
		}
	}
	return kbase.FormatIndexedFiles(entries, o)
}
