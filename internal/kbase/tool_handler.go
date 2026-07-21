package kbase

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/stream"
)

// ToolService is the narrow KBASE operation surface required by the five
// built-in tools. Manager satisfies this interface.
type ToolService interface {
	Search(ctx context.Context, agentKey string, query string, options SearchOptions) (SearchResult, error)
	Files(agentKey string, options FilesOptions) (FilesResult, error)
	Read(agentKey string, options ReadOptions) (ReadResult, error)
	Status(agentKey string) (Status, error)
	Refresh(ctx context.Context, agentKey string, options RefreshOptions) (RefreshResult, error)
}

type ToolHandler struct {
	service ToolService
}

func NewToolHandler(service ToolService) *ToolHandler {
	return &ToolHandler{service: service}
}

func (h *ToolHandler) ToolNames() []string {
	return []string{ToolSearch, ToolFiles, ToolRead, ToolStatus, ToolRefresh}
}

func (h *ToolHandler) Invoke(ctx context.Context, toolName string, args map[string]any, execCtx *contracts.ExecutionContext) (contracts.ToolExecutionResult, error) {
	agentKey, contextErr := h.requireContext(toolName, execCtx)
	if contextErr != nil {
		return *contextErr, nil
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case ToolSearch:
		return h.invokeSearch(ctx, agentKey, args)
	case ToolFiles:
		return h.invokeFiles(agentKey, args)
	case ToolRead:
		return h.invokeRead(agentKey, args)
	case ToolStatus:
		return h.invokeStatus(agentKey)
	case ToolRefresh:
		return h.invokeRefresh(ctx, agentKey, args)
	default:
		return contracts.ToolExecutionResult{Output: "tool not registered: " + toolName, Error: "tool_not_registered", ExitCode: -1}, nil
	}
}

func (h *ToolHandler) requireContext(toolName string, execCtx *contracts.ExecutionContext) (string, *contracts.ToolExecutionResult) {
	if h == nil || h.service == nil {
		return "", &contracts.ToolExecutionResult{Output: "kbase manager not configured", Error: "kbase_not_configured", ExitCode: -1}
	}
	if execCtx == nil || strings.TrimSpace(execCtx.Session.AgentKey) == "" {
		return "", &contracts.ToolExecutionResult{Output: toolName + " requires an active agent execution context", Error: "kbase_context_required", ExitCode: -1}
	}
	if !execCtx.Session.KBaseEnabled {
		return "", &contracts.ToolExecutionResult{Output: toolName + " requires the KBASE capability to be enabled for this agent", Error: "kbase_capability_disabled", ExitCode: -1}
	}
	return strings.TrimSpace(execCtx.Session.AgentKey), nil
}

func (h *ToolHandler) invokeSearch(ctx context.Context, agentKey string, args map[string]any) (contracts.ToolExecutionResult, error) {
	query := strings.TrimSpace(toolStringArg(args, "query"))
	if query == "" {
		return contracts.ToolExecutionResult{Output: "query must not be blank", Error: "missing_query", ExitCode: -1}, nil
	}
	result, err := h.service.Search(ctx, agentKey, query, SearchOptions{
		Limit:      int(toolInt64Arg(args, "limit")),
		Offset:     int(toolInt64Arg(args, "offset")),
		PathPrefix: strings.TrimSpace(toolStringArg(args, "pathPrefix")),
		PathGlob:   strings.TrimSpace(toolStringArg(args, "pathGlob")),
		Type:       strings.TrimSpace(toolStringArg(args, "type")),
	})
	if err != nil {
		return kbaseToolFailure(err), nil
	}
	toolResult := kbaseStructuredResult(map[string]any{
		"agentKey":   result.AgentKey,
		"query":      result.Query,
		"count":      result.Count,
		"matchCount": result.MatchCount,
		"offset":     result.Offset,
		"limit":      result.Limit,
		"truncated":  result.Truncated,
		"results":    result.Results,
		"stale":      result.Stale,
		"indexing":   result.Indexing,
	})
	if sources := searchHitSources(result.Results); len(sources) > 0 {
		publicationQuery := strings.TrimSpace(result.Query)
		if publicationQuery == "" {
			publicationQuery = query
		}
		toolResult.SourcePublication = &contracts.SourcePublication{
			Kind:    SourceKind,
			Query:   publicationQuery,
			Sources: sources,
		}
	}
	return toolResult, nil
}

func (h *ToolHandler) invokeFiles(agentKey string, args map[string]any) (contracts.ToolExecutionResult, error) {
	headLimit := -1
	if _, ok := args["head_limit"]; ok {
		headLimit = int(toolInt64Arg(args, "head_limit"))
	}
	result, err := h.service.Files(agentKey, FilesOptions{
		Mode:      strings.TrimSpace(toolStringArg(args, "mode")),
		Path:      strings.TrimSpace(toolStringArg(args, "path")),
		Pattern:   strings.TrimSpace(toolStringArg(args, "pattern")),
		Status:    strings.TrimSpace(toolStringArg(args, "status")),
		Type:      strings.TrimSpace(toolStringArg(args, "type")),
		Depth:     int(toolInt64Arg(args, "depth")),
		HeadLimit: headLimit,
		Offset:    int(toolInt64Arg(args, "offset")),
	})
	if err != nil {
		return kbaseToolFailure(err), nil
	}
	return kbaseStructuredResult(map[string]any{
		"tool":       result.Tool,
		"mode":       result.Mode,
		"path":       result.Path,
		"pattern":    result.Pattern,
		"status":     result.Status,
		"type":       result.Type,
		"matchCount": result.MatchCount,
		"fileCount":  result.FileCount,
		"dirCount":   result.DirCount,
		"truncated":  result.Truncated,
		"offset":     result.Offset,
		"headLimit":  result.HeadLimit,
		"results":    result.Results,
	}), nil
}

func (h *ToolHandler) invokeRead(agentKey string, args map[string]any) (contracts.ToolExecutionResult, error) {
	result, err := h.service.Read(agentKey, ReadOptions{
		ChunkID: strings.TrimSpace(toolStringArg(args, "chunkId")),
		Path:    strings.TrimSpace(toolStringArg(args, "path")),
		Offset:  int(toolInt64Arg(args, "offset")),
		Limit:   int(toolInt64Arg(args, "limit")),
	})
	if err != nil {
		return kbaseToolFailure(err), nil
	}
	return kbaseStructuredResult(map[string]any{
		"found":      result.Found,
		"chunkId":    result.ChunkID,
		"path":       result.Path,
		"heading":    result.Heading,
		"startLine":  result.StartLine,
		"endLine":    result.EndLine,
		"pageStart":  result.PageStart,
		"pageEnd":    result.PageEnd,
		"slideStart": result.SlideStart,
		"slideEnd":   result.SlideEnd,
		"sourceType": result.SourceType,
		"content":    result.Content,
	}), nil
}

func (h *ToolHandler) invokeStatus(agentKey string) (contracts.ToolExecutionResult, error) {
	status, err := h.service.Status(agentKey)
	if err != nil {
		return kbaseToolFailure(err), nil
	}
	payload := map[string]any{
		"agentKey":                  status.AgentKey,
		"mode":                      status.Mode,
		"storageLocation":           status.StorageLocation,
		"storageDir":                status.StorageDir,
		"sourceRoot":                status.SourceRoot,
		"workspaceRoot":             status.WorkspaceRoot,
		"indexing":                  status.Indexing,
		"stale":                     status.Stale,
		"degraded":                  status.Degraded,
		"files":                     status.Files,
		"chunks":                    status.Chunks,
		"embedding":                 status.Embedding,
		"chunk":                     status.Chunk,
		"lastRun":                   status.LastRun,
		"fileStats":                 status.FileStats,
		"engine":                    status.Engine,
		"schemaVersion":             status.SchemaVersion,
		"generation":                status.Generation,
		"indexes":                   status.Indexes,
		"sidecar":                   status.Sidecar,
		"pendingRecoveryOperations": status.PendingRecoveryOps,
		"pendingChanges":            status.PendingChanges,
		"storageDiskUsage":          status.StorageDiskUsage,
	}
	if status.LastIndexedAt != nil {
		payload["lastIndexedAt"] = *status.LastIndexedAt
	}
	if strings.TrimSpace(status.Error) != "" {
		payload["error"] = status.Error
	}
	return kbaseStructuredResult(payload), nil
}

func (h *ToolHandler) invokeRefresh(ctx context.Context, agentKey string, args map[string]any) (contracts.ToolExecutionResult, error) {
	result, err := h.service.Refresh(ctx, agentKey, RefreshOptions{
		Force: toolBoolArg(args, "force"),
		Mode:  "tool",
	})
	if err != nil {
		return kbaseToolFailure(err), nil
	}
	return kbaseStructuredResult(map[string]any{
		"agentKey":          result.AgentKey,
		"mode":              result.Mode,
		"status":            result.Status,
		"scope":             result.Scope,
		"candidatePaths":    result.CandidatePaths,
		"scannedFiles":      result.ScannedFiles,
		"changedFiles":      result.ChangedFiles,
		"newFiles":          result.NewFiles,
		"modifiedFiles":     result.ModifiedFiles,
		"metadataOnlyFiles": result.MetadataOnlyFiles,
		"unchangedFiles":    result.UnchangedFiles,
		"deletedFiles":      result.DeletedFiles,
		"indexedChunks":     result.IndexedChunks,
		"embeddedChunks":    result.EmbeddedChunks,
		"reusedChunks":      result.ReusedChunks,
		"pendingChanges":    result.PendingChanges,
		"error":             result.Error,
	}), nil
}

func kbaseToolFailure(err error) contracts.ToolExecutionResult {
	kind := KindOf(err)
	code := "kbase_invalid_request"
	switch kind {
	case ErrorUnavailable:
		code = "kbase_unavailable"
	case ErrorNotFound:
		code = "kbase_agent_not_found"
	case ErrorDisabled:
		code = "kbase_capability_disabled"
	}
	structured := map[string]any{"error": code}
	if kind == ErrorUnavailable {
		structured["stale"] = true
		structured["unavailable"] = true
	}
	return contracts.ToolExecutionResult{
		Output:     strings.TrimSpace(err.Error()),
		Structured: structured,
		Error:      code,
		ExitCode:   -1,
	}
}

func kbaseStructuredResult(payload map[string]any) contracts.ToolExecutionResult {
	encoded, _ := json.Marshal(payload)
	return contracts.ToolExecutionResult{Output: string(encoded), Structured: payload, ExitCode: 0}
}

func toolStringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func toolInt64Arg(args map[string]any, key string) int64 {
	switch value := args[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		number, _ := value.Int64()
		return number
	default:
		return 0
	}
}

func toolBoolArg(args map[string]any, key string) bool {
	value, ok := args[key].(bool)
	return ok && value
}

func searchHitSources(hits []SearchHit) []stream.Source {
	if len(hits) == 0 {
		return nil
	}
	normalizedHits := make([]SearchHit, 0, len(hits))
	for _, hit := range hits {
		hit.ChunkID = strings.TrimSpace(hit.ChunkID)
		hit.Path = strings.TrimSpace(hit.Path)
		hit.Heading = strings.TrimSpace(hit.Heading)
		hit.SourceType = strings.TrimSpace(hit.SourceType)
		hit.Snippet = strings.TrimSpace(hit.Snippet)
		hit.MatchType = strings.TrimSpace(hit.MatchType)
		if hit.ChunkID == "" && hit.Path == "" && hit.Snippet == "" {
			continue
		}
		normalizedHits = append(normalizedHits, hit)
	}
	if len(normalizedHits) == 0 {
		return nil
	}

	sourceIndexes := map[string]int{}
	sources := make([]stream.Source, 0, len(normalizedHits))
	for index, hit := range normalizedHits {
		key := hit.Path
		if key == "" {
			key = hit.ChunkID
		}
		if key == "" {
			continue
		}

		sourceIndex, ok := sourceIndexes[key]
		if !ok {
			name := filepath.Base(hit.Path)
			if name == "." || name == string(filepath.Separator) || strings.TrimSpace(name) == "" {
				name = key
			}
			title := hit.Path
			if title == "" {
				title = hit.Heading
			}
			sources = append(sources, stream.Source{
				ID:             SourceKind + ":" + key,
				Name:           name,
				Title:          title,
				Icon:           DefaultIconName,
				CollectionName: Mode,
			})
			sourceIndex = len(sources) - 1
			sourceIndexes[key] = sourceIndex
		}

		content := hit.Snippet
		if strings.TrimSpace(content) == "" {
			content = hit.Heading
		}
		sources[sourceIndex].Chunks = append(sources[sourceIndex].Chunks, stream.SourceChunk{
			ChunkID:    hit.ChunkID,
			Index:      index + 1,
			Content:    content,
			Score:      hit.Score,
			Path:       hit.Path,
			Heading:    hit.Heading,
			StartLine:  hit.StartLine,
			EndLine:    hit.EndLine,
			PageStart:  hit.PageStart,
			PageEnd:    hit.PageEnd,
			SlideStart: hit.SlideStart,
			SlideEnd:   hit.SlideEnd,
			SourceType: hit.SourceType,
			MatchType:  hit.MatchType,
		})
	}
	return sources
}
