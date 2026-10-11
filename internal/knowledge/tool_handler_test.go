package knowledge

import (
	"context"
	"encoding/json"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/contracts"
)

func (s *stubToolService) List() ([]api.KBaseOption, error) {
	s.called = ToolList
	return []api.KBaseOption{}, nil
}

type stubToolService struct {
	agentKey      string
	called        string
	searchOptions SearchOptions
	filesOptions  FilesOptions
	readOptions   ReadOptions
	searchResult  SearchResult
	filesResult   FilesResult
	readResult    ReadResult
	statusResult  Status
}

func (s *stubToolService) Search(_ context.Context, agentKey string, _ string, options SearchOptions) (SearchResult, error) {
	s.agentKey = agentKey
	s.searchOptions = options
	return s.searchResult, nil
}

func (s *stubToolService) Files(_ string, options FilesOptions) (FilesResult, error) {
	s.called = ToolFiles
	s.filesOptions = options
	return s.filesResult, nil
}

func (s *stubToolService) Read(_ string, options ReadOptions) (ReadResult, error) {
	s.called = ToolRead
	s.readOptions = options
	return s.readResult, nil
}

func (s *stubToolService) Status(_ string) (Status, error) {
	s.called = ToolStatus
	return s.statusResult, nil
}

func kbaseToolExecutionContext() *contracts.ExecutionContext {
	return &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "docs", Mode: Mode, KBaseBindingLibraryID: "docs"}}
}

func TestToolHandlerValidatesContextAndSearchQuery(t *testing.T) {
	cases := []struct {
		name    string
		handler *ToolHandler
		execCtx *contracts.ExecutionContext
		args    map[string]any
		want    string
	}{
		{name: "manager missing", handler: NewToolHandler(nil), execCtx: kbaseToolExecutionContext(), args: map[string]any{"libraryId": "docs", "query": "x"}, want: "kbase_not_configured"},
		{name: "context missing", handler: NewToolHandler(&stubToolService{}), args: map[string]any{"libraryId": "docs", "query": "x"}, want: "kbase_context_required"},
		{name: "unbound context missing library", handler: NewToolHandler(&stubToolService{}), execCtx: &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "docs", Mode: "REACT"}}, args: map[string]any{"query": "x"}, want: "missing_library_id"},
		{name: "query missing", handler: NewToolHandler(&stubToolService{}), execCtx: kbaseToolExecutionContext(), args: map[string]any{"libraryId": "docs", "query": "  "}, want: "missing_query"},
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
	result, err := NewToolHandler(service).Invoke(context.Background(), ToolSearch, map[string]any{"libraryId": "docs",
		"query": " policy ", "limit": float64(8), "pathPrefix": " docs/ ", "pathGlob": " **/*.md ", "type": " md ",
	}, kbaseToolExecutionContext())
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if result.ExitCode != 0 || result.Structured["agentKey"] != "docs" || result.Structured["results"] == nil {
		t.Fatalf("unexpected search wire %#v", result)
	}
	if service.searchOptions.Limit != 8 || service.searchOptions.Offset != 0 || service.searchOptions.PathPrefix != "docs/" || service.searchOptions.PathGlob != "**/*.md" || service.searchOptions.Type != "md" {
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
		filesResult:  FilesResult{LibraryID: "research", Tool: ToolFiles, Mode: "tree", HeadLimit: 25, Results: []FileEntry{{Type: "file", Path: "docs/a.md"}}},
		readResult:   ReadResult{LibraryID: "research", Found: true, ChunkID: "chunk_1", Path: "docs/a.md", Content: "text"},
		statusResult: Status{LibraryID: "research", AgentKey: "docs", Mode: Mode, Files: 1, Chunks: 2},
	}
	handler := NewToolHandler(service)
	execCtx := kbaseToolExecutionContext()

	files, err := handler.Invoke(context.Background(), ToolFiles, map[string]any{"libraryId": "docs",
		"mode": " tree ", "path": " docs ", "pattern": " ** ", "status": " active ", "type": " md ", "depth": float64(3), "headLimit": float64(25), "offset": float64(4),
	}, execCtx)
	if err != nil || files.Structured["tool"] != ToolFiles || service.filesOptions.HeadLimit != 25 || service.filesOptions.Depth != 3 || service.filesOptions.Offset != 4 {
		t.Fatalf("unexpected files result=%#v options=%#v err=%v", files, service.filesOptions, err)
	}
	if service.filesOptions.Mode != "tree" || service.filesOptions.Path != "docs" || service.filesOptions.Pattern != "**" || service.filesOptions.Status != "active" || service.filesOptions.Type != "md" {
		t.Fatalf("unexpected normalized files options %#v", service.filesOptions)
	}

	read, err := handler.Invoke(context.Background(), ToolRead, map[string]any{"libraryId": "docs", "chunkId": " chunk_1 ", "path": " docs/a.md ", "offset": float64(2), "limit": float64(10)}, execCtx)
	if err != nil || read.Structured["found"] != true || service.readOptions.ChunkID != "chunk_1" || service.readOptions.Path != "docs/a.md" || service.readOptions.Offset != 2 || service.readOptions.Limit != 10 {
		t.Fatalf("unexpected read result=%#v options=%#v err=%v", read, service.readOptions, err)
	}

	status, err := handler.Invoke(context.Background(), ToolStatus, map[string]any{"libraryId": "docs"}, execCtx)
	if err != nil || status.Structured["agentKey"] != "docs" || status.Structured["files"] != 1 {
		t.Fatalf("unexpected status result=%#v err=%v", status, err)
	}

	for _, result := range []contracts.ToolExecutionResult{files, read, status} {
		if result.Structured["libraryId"] != "research" {
			t.Fatalf("lost library identity: %+v", result)
		}
	}

	refresh, err := handler.Invoke(context.Background(), "kbase_refresh", map[string]any{"libraryId": "docs", "force": true}, execCtx)
	if err != nil || refresh.Error != "tool_not_registered" {
		t.Fatalf("removed refresh result=%#v err=%v", refresh, err)
	}
}

func TestToolHandlerFilesKeepsAbsentHeadLimitSentinel(t *testing.T) {
	service := &stubToolService{}
	_, err := NewToolHandler(service).Invoke(context.Background(), ToolFiles, map[string]any{"libraryId": "docs"}, kbaseToolExecutionContext())
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if service.filesOptions.HeadLimit != -1 {
		t.Fatalf("expected absent headLimit sentinel -1, got %#v", service.filesOptions)
	}
}

type unavailableToolService struct{ stubToolService }

func (unavailableToolService) Search(context.Context, string, string, SearchOptions) (SearchResult, error) {
	return SearchResult{}, &PolicyError{Kind: ErrorUnavailable, Message: "KBX CLI unavailable"}
}

func TestToolHandlerReturnsExplicitUnavailableResult(t *testing.T) {
	result, err := NewToolHandler(&unavailableToolService{}).Invoke(
		context.Background(), ToolSearch, map[string]any{"libraryId": "docs", "query": "policy"}, kbaseToolExecutionContext(),
	)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if result.Error != "kbase_unavailable" || result.ExitCode != -1 || result.Structured["stale"] != true || result.Structured["unavailable"] != true {
		t.Fatalf("unexpected unavailable result: %#v", result)
	}
}

func TestFilesReadStatusRejectUnknownFieldsBeforeService(t *testing.T) {
	for tool, known := range map[string]map[string]any{
		ToolFiles:  {"libraryId": "docs", "mode": "tree", "path": "docs/", "pattern": "**", "status": "active", "type": "md", "depth": 3, "headLimit": 0, "offset": 1},
		ToolRead:   {"libraryId": "docs", "path": "docs/a.md", "offset": 1, "limit": 20},
		ToolStatus: {"libraryId": "docs"},
	} {
		s := &stubToolService{}
		if r, err := NewToolHandler(s).Invoke(context.Background(), tool, known, kbaseToolExecutionContext()); err != nil || r.Error != "" || s.called != tool {
			t.Fatalf("%s declared fields: result=%+v called=%q err=%v", tool, r, s.called, err)
		}
		for _, name := range []string{"LibraryId", "agentKey", "head_limit"} {
			s = &stubToolService{}
			r, err := NewToolHandler(s).Invoke(context.Background(), tool, map[string]any{"libraryId": "docs", name: "x"}, kbaseToolExecutionContext())
			if err != nil || r.Error != "kbase_invalid_request" || s.called != "" {
				t.Fatalf("%s.%s: result=%+v called=%q err=%v", tool, name, r, s.called, err)
			}
		}
	}
}

func TestRetrievalToolsAlwaysRequireLibraryIDAndListNeedsNoBinding(t *testing.T) {
	service := &stubToolService{}
	handler := NewToolHandler(service)
	exec := &contracts.ExecutionContext{Session: contracts.QuerySession{AgentKey: "general", Mode: "GENERAL"}}
	for _, tool := range []string{ToolSearch, ToolFiles, ToolRead, ToolStatus} {
		for _, value := range []any{nil, " ", 42} {
			args := map[string]any{"query": "x"}
			if value != nil {
				args["libraryId"] = value
			}
			result, err := handler.Invoke(context.Background(), tool, args, exec)
			if err != nil || result.Error != "missing_library_id" {
				t.Fatalf("%s accepted missing libraryId: %+v %v", tool, result, err)
			}
		}
	}
	result, err := handler.Invoke(context.Background(), ToolList, map[string]any{}, exec)
	if err != nil || result.Error != "" || service.called != ToolList {
		t.Fatalf("unbound discovery: %+v %v", result, err)
	}
}

func TestHandlerUsesFrozenRetrievalOverridesOnlyForBoundLibrary(t *testing.T) {
	topK := 8 // An explicit baseline value must still override a different library default.
	cfg := DefaultConfig()
	cfg.LibraryID = "bound"
	cfg.RetrievalOverrides = &RetrievalSettings{TopK: &topK}
	snapshot, err := json.Marshal(struct {
		Config    Config
		Overrides *RetrievalSettings
	}{cfg, cfg.RetrievalOverrides})
	if err != nil {
		t.Fatal(err)
	}
	session := contracts.QuerySession{AgentKey: "general", KBaseBindingLibraryID: "bound", KBaseRetrievalConfig: string(snapshot)}
	topK = 3 // Mutating the definition must not change the running session's override.
	for _, id := range []string{"bound", "temporary"} {
		service := &stubToolService{}
		result, err := NewToolHandler(service).Invoke(context.Background(), ToolSearch, map[string]any{"libraryId": id, "query": "x"}, &contracts.ExecutionContext{Session: session})
		if err != nil || result.Error != "" {
			t.Fatalf("search: %+v %v", result, err)
		}
		if id == "bound" {
			o := service.searchOptions.AgentConfig
			if o == nil || o.RetrievalOverrides == nil || *o.RetrievalOverrides.TopK != 8 {
				t.Fatalf("frozen override lost: %+v", o)
			}
		} else if service.searchOptions.AgentConfig != nil {
			t.Fatal("fixed Agent overrides applied to temporary library")
		}
	}
}
