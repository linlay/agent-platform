package kbase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/timecontract"
)

const (
	defaultFilesHeadLimit = 100
	defaultFilesTreeDepth = 2
	maxFilesTreeDepth     = 8
)

func (m *Manager) Files(agentKey string, options FilesOptions) (FilesResult, error) {
	if err := m.capabilityDegradedError(agentKey); err != nil {
		return FilesResult{}, err
	}
	cfg, _, err := m.resolve(agentKey)
	if err != nil {
		return FilesResult{}, err
	}
	normalized, err := normalizeFilesOptions(options)
	if err != nil {
		return FilesResult{}, err
	}
	result := emptyFilesResult(normalized)
	control, err := OpenReadControlStore(cfg.StorageDir)
	if err != nil {
		if os.IsNotExist(err) {
			return FilesResult{}, &PolicyError{Kind: ErrorUnavailable, Message: "KBASE LanceDB generation is not ready"}
		}
		return FilesResult{}, err
	}
	defer control.Close()
	generation, err := control.ActiveGeneration(context.Background())
	if err != nil {
		return FilesResult{}, err
	}
	if generation == nil {
		return FilesResult{}, &PolicyError{Kind: ErrorUnavailable, Message: "KBASE LanceDB generation is not ready"}
	}
	records, err := control.Files(context.Background(), generation.ID)
	if err != nil {
		return FilesResult{}, err
	}
	return filesResultFromRecords(result, normalized, records)
}

func filesResultFromRecords(result FilesResult, normalized FilesOptions, records []fileRecord) (FilesResult, error) {
	files := filterFileRecords(records, normalized)
	result.FileCount = len(files)
	if normalized.Mode == "tree" {
		entries, err := buildFileTreeEntries(files, normalized.Path, normalized.Depth)
		if err != nil {
			return FilesResult{}, err
		}
		result.DirCount = countDirEntries(entries)
		result.MatchCount = len(entries)
		result.Results, result.Truncated = pageFileEntries(entries, normalized.Offset, normalized.HeadLimit)
		return result, nil
	}
	entries := make([]FileEntry, 0, len(files))
	for _, rec := range files {
		entry, err := fileRecordEntry(rec)
		if err != nil {
			return FilesResult{}, err
		}
		entries = append(entries, entry)
	}
	result.MatchCount = len(entries)
	result.Results, result.Truncated = pageFileEntries(entries, normalized.Offset, normalized.HeadLimit)
	return result, nil
}

func normalizeFilesOptions(options FilesOptions) (FilesOptions, error) {
	mode := strings.ToLower(strings.TrimSpace(options.Mode))
	if mode == "" {
		mode = "files"
	}
	if mode != "files" && mode != "tree" {
		return FilesOptions{}, fmt.Errorf("mode must be files or tree")
	}
	status := strings.ToLower(strings.TrimSpace(options.Status))
	if status == "" {
		status = "active"
	}
	switch status {
	case "active", "skipped", "error", "deleted", "all":
	default:
		return FilesOptions{}, fmt.Errorf("status must be active, skipped, error, deleted, or all")
	}
	pattern := normalizeKBaseGlob(options.Pattern)
	if pattern == "" {
		pattern = "**"
	}
	depth := options.Depth
	if depth <= 0 {
		depth = defaultFilesTreeDepth
	}
	if depth > maxFilesTreeDepth {
		depth = maxFilesTreeDepth
	}
	offset := options.Offset
	if offset < 0 {
		offset = 0
	}
	headLimit := options.HeadLimit
	if headLimit < 0 {
		headLimit = defaultFilesHeadLimit
	}
	return FilesOptions{
		Mode:      mode,
		Path:      normalizeIndexedPath(options.Path),
		Pattern:   pattern,
		Status:    status,
		Type:      normalizeKBaseExt(options.Type),
		Depth:     depth,
		HeadLimit: headLimit,
		Offset:    offset,
	}, nil
}

func emptyFilesResult(options FilesOptions) FilesResult {
	return FilesResult{
		Tool:      "kbase_files",
		Mode:      options.Mode,
		Path:      options.Path,
		Pattern:   options.Pattern,
		Status:    options.Status,
		Type:      strings.TrimPrefix(options.Type, "."),
		Offset:    options.Offset,
		HeadLimit: options.HeadLimit,
		Results:   []FileEntry{},
	}
}

func filterFileRecords(records []fileRecord, options FilesOptions) []fileRecord {
	matchers := compileMatchers([]string{options.Pattern})
	out := make([]fileRecord, 0, len(records))
	for _, rec := range records {
		if options.Status != "all" && !strings.EqualFold(rec.Status, options.Status) {
			continue
		}
		if options.Type != "" && strings.ToLower(rec.Ext) != options.Type {
			continue
		}
		rel, ok := relativeToPrefix(rec.Path, options.Path)
		if !ok {
			continue
		}
		if len(matchers) > 0 && !matchesAny(matchers, rel) {
			continue
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func fileRecordEntry(rec fileRecord) (FileEntry, error) {
	path := normalizeIndexedPath(rec.Path)
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "." {
		dir = ""
	} else if dir != "" {
		dir += "/"
	}
	mtimeMs, err := optionalPublicFileEpochMillis(rec.MTimeMS, "mtimeMs", "kbase.files")
	if err != nil {
		return FileEntry{}, err
	}
	indexedAt, err := optionalPublicFileEpochMillis(rec.IndexedAt, "indexedAt", "kbase.files")
	if err != nil {
		return FileEntry{}, err
	}
	return FileEntry{
		Type:       "file",
		Path:       path,
		Name:       filepath.Base(path),
		Dir:        dir,
		Ext:        rec.Ext,
		Mime:       rec.Mime,
		Size:       rec.Size,
		MTimeMS:    mtimeMs,
		TextSHA256: rec.TextSHA256,
		Extractor:  rec.Extractor,
		Status:     rec.Status,
		SkipReason: rec.SkipReason,
		Error:      rec.Error,
		ChunkCount: rec.ChunkCount,
		IndexedAt:  indexedAt,
	}, nil
}

func buildFileTreeEntries(records []fileRecord, prefix string, depth int) ([]FileEntry, error) {
	dirs := map[string]*FileEntry{}
	files := []FileEntry{}
	for _, rec := range records {
		rel, ok := relativeToPrefix(rec.Path, prefix)
		if !ok || rel == "" {
			continue
		}
		parts := strings.Split(rel, "/")
		for i := 0; i < len(parts)-1 && i < depth; i++ {
			dirRel := strings.Join(parts[:i+1], "/") + "/"
			dirPath := joinIndexedPath(prefix, dirRel)
			entry := dirs[dirPath]
			if entry == nil {
				name := strings.TrimSuffix(parts[i], "/")
				dirs[dirPath] = &FileEntry{Type: "dir", Path: dirPath, Name: name, Depth: i + 1}
				entry = dirs[dirPath]
			}
			entry.FileCount++
			entry.ChunkCount += rec.ChunkCount
		}
		fileDepth := len(parts)
		if fileDepth <= depth {
			entry, err := fileRecordEntry(rec)
			if err != nil {
				return nil, err
			}
			entry.Depth = fileDepth
			files = append(files, entry)
		}
	}
	entries := make([]FileEntry, 0, len(dirs)+len(files))
	for _, entry := range dirs {
		entries = append(entries, *entry)
	}
	entries = append(entries, files...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Path != entries[j].Path {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].Type < entries[j].Type
	})
	return entries, nil
}

// KBASE uses 0 for a not-yet-indexed file and -1 as an internal pending
// recovery marker. Neither represents a public instant, so omit them. Any
// other invalid persisted value (notably Unix seconds) is corrupt history and
// must fail instead of being exposed or repaired.
func optionalPublicFileEpochMillis(value int64, field string, location string) (*int64, error) {
	if value == 0 || value == -1 {
		return nil, nil
	}
	if err := timecontract.ValidateEpochMillis(value, field, location+"."+field); err != nil {
		return nil, err
	}
	result := value
	return &result, nil
}

func joinIndexedPath(prefix string, rel string) string {
	prefix = normalizeIndexedPath(prefix)
	rel = strings.TrimLeft(filepath.ToSlash(strings.TrimSpace(rel)), "/")
	if prefix == "" {
		return rel
	}
	if rel == "" {
		return prefix
	}
	return prefix + "/" + rel
}

func countDirEntries(entries []FileEntry) int {
	count := 0
	for _, entry := range entries {
		if entry.Type == "dir" {
			count++
		}
	}
	return count
}

func pageFileEntries(entries []FileEntry, offset int, headLimit int) ([]FileEntry, bool) {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(entries) {
		return []FileEntry{}, false
	}
	if headLimit == 0 {
		return entries[offset:], false
	}
	end := offset + headLimit
	if end >= len(entries) {
		return entries[offset:], false
	}
	return entries[offset:end], true
}
