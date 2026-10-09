package kbx

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"agent-platform/internal/knowledge"
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
	Results          []searchDocument
	Trace            struct {
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

type searchDocument struct {
	File     string
	Title    string
	Score    float64
	ResultID string `json:"resultId"`
	Chunk    struct {
		ID    string
		Range textRange
	}
	Evidence evidence
	Explain  struct {
		Graph *graphExplanation `json:"graph"`
	}
}

type graphExplanation struct {
	Links           []knowledge.GraphLink `json:"links"`
	SupportingPaths int                   `json:"supportingPaths"`
	Score           map[string]float64    `json:"score"`
	BestPath        *struct {
		Score float64
		Nodes []knowledge.GraphNode
		Edges []struct {
			Predicate  string
			Confidence float64
			Evidence   []struct {
				File     string
				Evidence evidence
			}
		}
	} `json:"bestPath"`
}

func predicate(op, value string) map[string]any {
	return map[string]any{"op": op, "key": "sys.path", "value": value}
}
func appendFilter(args []string, v any) []string {
	b, _ := json.Marshal(v)
	return append(args, "--filter", string(b))
}
func (m *Manager) Search(ctx context.Context, key, query string, o knowledge.SearchOptions) (knowledge.SearchResult, error) {
	o, err := knowledge.NormalizeSearchOptions(o)
	if err != nil {
		return knowledge.SearchResult{}, err
	}
	l, err := m.resolve(key)
	if err != nil {
		return knowledge.SearchResult{}, err
	}
	defer l.release()
	if strings.TrimSpace(query) == "" {
		return knowledge.SearchResult{}, fmt.Errorf("query must not be blank")
	}
	limit := o.Limit
	if limit <= 0 {
		limit = l.spec.Config.Retrieval.TopK
	}
	if limit <= 0 {
		limit = 8
	}
	if limit > 50 {
		return knowledge.SearchResult{}, fmt.Errorf("limit must be at most 50")
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
	if o.CandidateLimit != 0 {
		candidate = o.CandidateLimit
		if candidate < limit {
			return knowledge.SearchResult{}, fmt.Errorf("candidateLimit must be at least the effective limit (%d)", limit)
		}
		if ceiling := l.spec.Config.Retrieval.CandidateMax; ceiling > 0 && candidate > max(limit, ceiling) {
			return knowledge.SearchResult{}, fmt.Errorf("candidateLimit exceeds the Agent candidate budget (%d)", max(limit, ceiling))
		}
	}
	args := []string{o.Method, "--agent", "-n", strconv.Itoa(limit)}
	for _, c := range l.definition.Collections {
		args = append(args, "-c", c.Name)
	}
	if o.Method == "gsearch" {
		// Graph search has a distinct result envelope and does not accept --full
		// or a candidate budget. Explanations carry the actual relationship evidence.
		args = append(args, "--explain")
		for _, entity := range o.Entities {
			args = append(args, "--entity="+entity)
		}
		for _, relation := range o.Relations {
			args = append(args, "--relation="+relation)
		}
		if o.Direction != "" {
			args = append(args, "--direction", o.Direction)
		}
		if o.MaxHops != 0 {
			args = append(args, "--max-hops", strconv.Itoa(o.MaxHops))
		}
	} else {
		args = append(args, "--full", "-C", strconv.Itoa(candidate))
		if o.Method == "query" {
			// Deployment configuration currently provides embedding only. Graph
			// recall can use an already-built local graph without an extraction model.
			args = append(args, "--no-rerank", "--explain")
			if o.NoGraph {
				args = append(args, "--no-graph")
			}
		}
		for _, exclude := range o.Exclude {
			args = append(args, "--exclude="+exclude)
		}
		if o.Intent != "" {
			args = append(args, "--intent="+o.Intent)
		}
		for _, field := range []struct {
			flag  string
			value *float64
		}{{"--min-score", o.MinScore}, {"--recency-weight", o.RecencyWeight}, {"--recency-half-life-days", o.RecencyHalfLifeDays}} {
			if field.value != nil {
				args = append(args, field.flag+"="+strconv.FormatFloat(*field.value, 'g', -1, 64))
			}
		}
	}
	if o.Filter != "" {
		// A separate filter argument intersects (never replaces) Agent policy.
		args = append(args, "--filter="+o.Filter)
	}
	for _, f := range []struct{ op, value string }{{"pathPrefix", o.PathPrefix}, {"pathGlob", o.PathGlob}} {
		if f.value != "" {
			p, err := scopedPathPredicate(l, f.op, f.value)
			if err != nil {
				return knowledge.SearchResult{}, err
			}
			args = appendFilter(args, p)
		}
	}
	if o.Type != "" {
		args = appendFilter(args, predicate("extension", o.Type))
	}
	args = appendFilter(args, libraryPolicy(l))
	args = append(args, "--", query)
	var raw json.RawMessage
	if err = m.call(ctx, l, o.Method == "query" || o.Method == "vsearch", &raw, args...); err != nil {
		return knowledge.SearchResult{}, err
	}
	var response searchResponse
	if o.Method == "gsearch" {
		if err = json.Unmarshal(raw, &response.Results); err != nil || response.Results == nil {
			return knowledge.SearchResult{}, unavailable("KBX graph search must return a document array")
		}
		response.Trace.Coverage.RetrievalUsed = []string{"graph"}
	} else {
		if err = json.Unmarshal(raw, &response); err != nil || response.Type != "kbx.search.response" || response.RetrievalVersion != 6 || response.Trace.ResultUnit != "chunk" {
			return knowledge.SearchResult{}, unavailable("KBX retrieval contract 6 with chunk results is required")
		}
	}
	result := knowledge.SearchResult{LibraryID: l.spec.Config.LibraryID, AgentKey: key, Query: query, Method: o.Method, Limit: limit, Results: []knowledge.SearchHit{}, Engine: "kbx", Stale: l.definition.Stale, Degraded: response.Trace.Degraded || l.definition.Degraded, CandidateBudgetExhausted: response.Trace.CandidateBudgetExhausted}
	result.Stale = l.definition.Stale
	result.Indexing = l.definition.Indexing
	result.Degraded = result.Degraded || l.definition.Degraded
	for _, hit := range response.Results {
		p, err := allowedDocumentPath(l, hit.File)
		if err != nil {
			return knowledge.SearchResult{}, err
		}
		if hit.Chunk.ID == "" || hit.Evidence.ID == "" {
			return knowledge.SearchResult{}, unavailable("KBX result has no chunk/evidence locator")
		}
		graph, err := mapGraphExplanation(l, hit.Explain.Graph)
		if err != nil {
			return knowledge.SearchResult{}, err
		}
		lines := hit.Chunk.Range
		matchType := "kbx"
		if o.Method == "gsearch" {
			// Graph snippets may cover only part of the chunk. Cite the returned
			// evidence range; keep both locators so either range can be read back.
			lines = hit.Evidence.Range
			matchType = "graph"
		}
		result.Results = append(result.Results, knowledge.SearchHit{LibraryID: l.spec.Config.LibraryID, ChunkID: hit.Chunk.ID, ResultID: hit.ResultID, EvidenceID: hit.Evidence.ID, Path: p, Heading: hit.Title, StartLine: lines.LineStart, EndLine: lines.LineEnd, SourceType: strings.TrimPrefix(strings.ToLower(path.Ext(p)), "."), Snippet: hit.Evidence.Text, Score: hit.Score, MatchType: matchType, Graph: graph})
	}
	result.RetrievalChannels = response.Trace.Coverage.RetrievalUsed
	result.OptionalUnavailable = response.Trace.Coverage.OptionalUnavailable
	result.Count = len(result.Results)
	return result, nil
}

func mapGraphExplanation(l library, input *graphExplanation) (*knowledge.GraphExplanation, error) {
	if input == nil {
		return nil, nil
	}
	output := &knowledge.GraphExplanation{Links: input.Links, SupportingPaths: input.SupportingPaths, Score: input.Score}
	if input.BestPath == nil {
		return output, nil
	}
	output.BestPath = &knowledge.GraphPath{Score: input.BestPath.Score, Nodes: input.BestPath.Nodes, Edges: []knowledge.GraphEdge{}}
	for _, edge := range input.BestPath.Edges {
		mapped := knowledge.GraphEdge{Predicate: edge.Predicate, Confidence: edge.Confidence, Evidence: []knowledge.GraphEvidence{}}
		for _, item := range edge.Evidence {
			p, err := allowedDocumentPath(l, item.File)
			if err != nil {
				return nil, err
			}
			if !evidencePattern.MatchString(item.Evidence.ID) {
				return nil, unavailable("KBX graph evidence has no valid content-addressed locator")
			}
			mapped.Evidence = append(mapped.Evidence, knowledge.GraphEvidence{Path: p, EvidenceID: item.Evidence.ID, StartLine: item.Evidence.Range.LineStart, EndLine: item.Evidence.Range.LineEnd, Content: item.Evidence.Text})
		}
		output.BestPath.Edges = append(output.BestPath.Edges, mapped)
	}
	return output, nil
}
func documentPath(uri string) (string, error) {
	const prefix = "kbx://"
	if !strings.HasPrefix(uri, prefix) {
		return "", unavailable("KBX returned a document outside the library")
	}
	return relativePath(strings.TrimPrefix(uri, prefix))
}
func relativePath(p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") || strings.Contains(p, ":") {
		return "", fmt.Errorf("path must be collection-relative")
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

func (m *Manager) Read(key string, o knowledge.ReadOptions) (knowledge.ReadResult, error) {
	return m.readBound(key, "", o)
}
func (m *Manager) readBound(key, expectedLibrary string, o knowledge.ReadOptions) (knowledge.ReadResult, error) {
	l, err := m.resolve(key)
	if err != nil {
		return knowledge.ReadResult{}, err
	}
	defer l.release()
	if expectedLibrary != "" && expectedLibrary != l.spec.Config.LibraryID {
		return knowledge.ReadResult{}, fmt.Errorf("Agent library binding changed")
	}
	ctx, cancel := readerContext()
	defer cancel()
	args := []string{"get", "--agent", "--no-line-numbers"}
	if o.ChunkID != "" {
		if !evidencePattern.MatchString(o.ChunkID) {
			return knowledge.ReadResult{}, fmt.Errorf("chunkId must be a KBX content-addressed locator")
		}
		if o.Offset != 0 || o.Limit != 0 {
			return knowledge.ReadResult{}, fmt.Errorf("chunkId returns its exact range; use path for line pagination")
		}
		args = append(args, "--evidence", o.ChunkID)
	} else {
		p, e := relativePath(o.Path)
		if e != nil {
			return knowledge.ReadResult{}, e
		}
		n := o.Limit
		if n <= 0 {
			n = 80
		}
		if n > 2000 {
			n = 2000
		}
		if o.Offset < 0 {
			return knowledge.ReadResult{}, fmt.Errorf("offset must not be negative")
		}
		if _, e := allowedDocumentPath(l, "kbx://"+p); e != nil {
			return knowledge.ReadResult{}, e
		}
		args = append(args, "--from", strconv.Itoa(max(1, o.Offset)), "--lines", strconv.Itoa(n), "--", "kbx://"+p)
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
		return knowledge.ReadResult{}, err
	}
	p, err := allowedDocumentPath(l, r.File)
	if err != nil {
		return knowledge.ReadResult{}, err
	}
	if o.Path != "" && p != o.Path {
		return knowledge.ReadResult{}, fmt.Errorf("evidence does not belong to requested path")
	}
	content := r.Evidence.Text
	if content == "" {
		content = r.Body
	}
	return knowledge.ReadResult{LibraryID: l.spec.Config.LibraryID, Found: true, ChunkID: o.ChunkID, Path: p, Heading: r.Title, StartLine: r.ReadRange.From, EndLine: r.ReadRange.Through, Content: content, HasMore: r.ReadRange.HasMore, NextEvidence: r.ReadRange.NextEvidence}, nil
}
func (m *Manager) Files(key string, o knowledge.FilesOptions) (knowledge.FilesResult, error) {
	l, err := m.resolve(key)
	if err != nil {
		return knowledge.FilesResult{}, err
	}
	defer l.release()
	if o.Status != "" && o.Status != "active" {
		return knowledge.FilesResult{}, fmt.Errorf("KBX exposes active indexed files only")
	}
	ctx, cancel := readerContext()
	defer cancel()
	entries := []knowledge.FileEntry{}
	for _, c := range l.definition.Collections {
		var r struct {
			Complete  bool
			Documents []struct{ File string }
		}
		if err = m.call(ctx, l, false, &r, "ls", "kbx://"+c.Name, "--agent"); err != nil {
			return knowledge.FilesResult{}, err
		}
		if !r.Complete {
			return knowledge.FilesResult{}, unavailable("KBX file inventory is incomplete")
		}
		for _, d := range r.Documents {
			p, e := allowedDocumentPath(l, d.File)
			if e != nil {
				return knowledge.FilesResult{}, e
			}
			entries = append(entries, knowledge.FileEntry{Path: p, Ext: strings.ToLower(path.Ext(p)), Status: "active"})
		}
	}
	result, err := knowledge.FormatIndexedFiles(entries, o)
	result.LibraryID = l.spec.Config.LibraryID
	return result, err
}
