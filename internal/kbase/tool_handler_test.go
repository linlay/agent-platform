package kbase

import (
	"context"
	"testing"

	"agent-platform/internal/contracts"
)

type stubToolService struct {
	agentKey       string
	searchOptions  SearchOptions
	filesOptions   FilesOptions
	readOptions    ReadOptions
	refreshOptions RefreshOptions
	searchResult   SearchResult
	filesResult    FilesResult
	readResult     ReadResult
	statusResult   Status
	refreshResult  RefreshResult
}

func (s *stubToolService) Search(_ context.Context, agentKey string, _ string, options SearchOptions) (SearchResult, error) {
	s.agentKey = agentKey
	s.searchOptions = options
	return s.searchResult, nil
}

func (s *stubToolService) Files(_ string, options FilesOptions) (FilesResult, error) {
	s.filesOptions = options
	return s.filesResult, nil
}

func (s *stubToolService) Read(_ string, options ReadOptions) (ReadResult, error) {
	s.readOptions = options
	return s.readResult, nil
}

func (s *stubToolService) Status(_ string) (Status, error) {
	return s.statusResult, nil
}

func (s *stubToolService) Refresh(_ context.Context, _ string, options RefreshOptions) (RefreshResult, error) {
	s.refreshOptions = options
	return s.refreshResult, nil
}

func kbaseToolExecutionContext() *contracts.ExecutionContext {
	return &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "docs", Mode: Mode, KBaseEnabled: true}}
}

func TestToolHandlerValidatesContextAndSearchQuery(t *testing.T) {
	cases := []struct {
		name    string
		handler *ToolHandler
		execCtx *contracts.ExecutionContext
		args    map[string]any
		want    string
	}{
		{name: "manager missing", handler: NewToolHandler(nil), execCtx: kbaseToolExecutionContext(), args: map[string]any{"query": "x"}, want: "kbase_not_configured"},
		{name: "context missing", handler: NewToolHandler(&stubToolService{}), args: map[string]any{"query": "x"}, want: "kbase_context_required"},
		{name: "disabled capability", handler: NewToolHandler(&stubToolService{}), execCtx: &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "docs", Mode: "REACT"}}, args: map[string]any{"query": "x"}, want: "kbase_capability_disabled"},
		{name: "query missing", handler: NewToolHandler(&stubToolService{}), execCtx: kbaseToolExecutionContext(), args: map[string]any{"query": "  "}, want: "missing_query"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.handler.Invoke(context.Background(), ToolSearch, tc.args, tc.execCtx)
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			if result.Error != tc.want || result.ExitCode != -1 {
				t.Fatalf("unexpected validation result %#v", result)
			}
		})
	}
}

func TestToolHandlerSearchPreservesWireAndPublishesSources(t *testing.T) {
	service := &stubToolService{searchResult: SearchResult{
		AgentKey:   "docs",
		Query:      "policy",
		Count:      2,
		MatchCount: 2,
		Limit:      8,
		Results: []SearchHit{
			{ChunkID: "chunk_1", Path: "docs/policy.md", Heading: "Policy", StartLine: 4, EndLine: 9, Snippet: "first", Score: 0.9, MatchType: "hybrid"},
			{ChunkID: "chunk_2", Path: "docs/policy.md", StartLine: 14, EndLine: 18, Snippet: "second", Score: 0.7, MatchType: "fts"},
		},
	}}
	result, err := NewToolHandler(service).Invoke(context.Background(), ToolSearch, map[string]any{
		"query": " policy ", "agentKey": "other-agent", "limit": float64(8), "offset": float64(2), "pathPrefix": " docs/ ", "pathGlob": " **/*.md ", "type": " md ",
	}, kbaseToolExecutionContext())
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if result.ExitCode != 0 || result.Structured["agentKey"] != "docs" || result.Structured["results"] == nil {
		t.Fatalf("unexpected search wire %#v", result)
	}
	if service.searchOptions.Limit != 8 || service.searchOptions.Offset != 2 || service.searchOptions.PathPrefix != "docs/" || service.searchOptions.PathGlob != "**/*.md" || service.searchOptions.Type != "md" {
		t.Fatalf("unexpected search options %#v", service.searchOptions)
	}
	if service.agentKey != "docs" {
		t.Fatalf("tool escaped session agent key: got %q", service.agentKey)
	}
	publication := result.SourcePublication
	if publication == nil || publication.Kind != "kbase" || publication.Query != "policy" || len(publication.Sources) != 1 {
		t.Fatalf("unexpected source publication %#v", publication)
	}
	source := publication.Sources[0]
	if source.ID != "kbase:docs/policy.md" || source.Name != "policy.md" || len(source.Chunks) != 2 {
		t.Fatalf("unexpected source %#v", source)
	}
	if source.Chunks[0].Index != 1 || source.Chunks[1].Index != 2 || source.Chunks[1].MatchType != "fts" {
		t.Fatalf("unexpected source chunks %#v", source.Chunks)
	}
}

func TestToolHandlerFilesReadStatusAndRefresh(t *testing.T) {
	service := &stubToolService{
		filesResult:   FilesResult{Tool: ToolFiles, Mode: "tree", HeadLimit: 25, Results: []FileEntry{{Type: "file", Path: "docs/a.md"}}},
		readResult:    ReadResult{Found: true, ChunkID: "chunk_1", Path: "docs/a.md", Content: "text"},
		statusResult:  Status{AgentKey: "docs", Mode: Mode, Files: 1, Chunks: 2},
		refreshResult: RefreshResult{AgentKey: "docs", Mode: "tool", Status: "completed", ScannedFiles: 1},
	}
	handler := NewToolHandler(service)
	execCtx := kbaseToolExecutionContext()

	files, err := handler.Invoke(context.Background(), ToolFiles, map[string]any{
		"mode": " tree ", "path": " docs ", "pattern": " ** ", "status": " active ", "type": " md ", "depth": float64(3), "head_limit": float64(25), "offset": float64(4),
	}, execCtx)
	if err != nil || files.Structured["tool"] != ToolFiles || service.filesOptions.HeadLimit != 25 || service.filesOptions.Depth != 3 || service.filesOptions.Offset != 4 {
		t.Fatalf("unexpected files result=%#v options=%#v err=%v", files, service.filesOptions, err)
	}
	if service.filesOptions.Mode != "tree" || service.filesOptions.Path != "docs" || service.filesOptions.Pattern != "**" || service.filesOptions.Status != "active" || service.filesOptions.Type != "md" {
		t.Fatalf("unexpected normalized files options %#v", service.filesOptions)
	}

	read, err := handler.Invoke(context.Background(), ToolRead, map[string]any{"chunkId": " chunk_1 ", "path": " docs/a.md ", "offset": float64(2), "limit": float64(10)}, execCtx)
	if err != nil || read.Structured["found"] != true || service.readOptions.ChunkID != "chunk_1" || service.readOptions.Path != "docs/a.md" || service.readOptions.Offset != 2 || service.readOptions.Limit != 10 {
		t.Fatalf("unexpected read result=%#v options=%#v err=%v", read, service.readOptions, err)
	}

	status, err := handler.Invoke(context.Background(), ToolStatus, nil, execCtx)
	if err != nil || status.Structured["agentKey"] != "docs" || status.Structured["files"] != 1 {
		t.Fatalf("unexpected status result=%#v err=%v", status, err)
	}

	refresh, err := handler.Invoke(context.Background(), ToolRefresh, map[string]any{"force": true}, execCtx)
	if err != nil || refresh.Structured["status"] != "completed" || !service.refreshOptions.Force || service.refreshOptions.Mode != "tool" {
		t.Fatalf("unexpected refresh result=%#v options=%#v err=%v", refresh, service.refreshOptions, err)
	}
}

func TestToolHandlerFilesKeepsAbsentHeadLimitSentinel(t *testing.T) {
	service := &stubToolService{}
	_, err := NewToolHandler(service).Invoke(context.Background(), ToolFiles, map[string]any{}, kbaseToolExecutionContext())
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if service.filesOptions.HeadLimit != -1 {
		t.Fatalf("expected absent head_limit sentinel -1, got %#v", service.filesOptions)
	}
}

type unavailableToolService struct{ stubToolService }

func (unavailableToolService) Search(context.Context, string, string, SearchOptions) (SearchResult, error) {
	return SearchResult{}, &PolicyError{Kind: ErrorUnavailable, Message: "KBASE sidecar unavailable"}
}

func TestToolHandlerReturnsExplicitUnavailableResult(t *testing.T) {
	result, err := NewToolHandler(&unavailableToolService{}).Invoke(
		context.Background(), ToolSearch, map[string]any{"query": "policy"}, kbaseToolExecutionContext(),
	)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if result.Error != "kbase_unavailable" || result.ExitCode != -1 || result.Structured["stale"] != true || result.Structured["unavailable"] != true {
		t.Fatalf("unexpected unavailable result: %#v", result)
	}
}
