package kbase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"agent-platform/internal/catalog"
	"agent-platform/internal/config"
)

const (
	schemaVersion       = "2"
	defaultMaxFileBytes = 50 * 1024 * 1024
)

var supportedTextExtensions = map[string]struct{}{
	".md":       {},
	".markdown": {},
	".txt":      {},
	".rst":      {},
	".adoc":     {},
	".csv":      {},
	".json":     {},
	".yaml":     {},
	".yml":      {},
	".go":       {},
	".ts":       {},
	".tsx":      {},
	".js":       {},
	".jsx":      {},
	".py":       {},
	".java":     {},
	".rs":       {},
	".sh":       {},
}

type resolvedConfig struct {
	AgentKey      string
	WorkspaceRoot string
	StorageDir    string
	Storage       string
	Embedding     EmbeddingSnapshot
	Include       []string
	Exclude       []string
	Chunk         catalog.AgentKBaseChunkConfig
	Retrieval     catalog.AgentKBaseRetrievalConfig
	Extraction    config.KBaseExtractionConfig
	ConfigHash    string
}

type manifest struct {
	SchemaVersion string                            `json:"schemaVersion"`
	AgentKey      string                            `json:"agentKey"`
	WorkspaceRoot string                            `json:"workspaceRoot"`
	ConfigHash    string                            `json:"configHash"`
	Embedding     EmbeddingSnapshot                 `json:"embedding"`
	Include       []string                          `json:"include"`
	Exclude       []string                          `json:"exclude"`
	Chunk         catalog.AgentKBaseChunkConfig     `json:"chunk"`
	Retrieval     catalog.AgentKBaseRetrievalConfig `json:"retrieval"`
	Extraction    config.KBaseExtractionConfig      `json:"extraction"`
	Storage       string                            `json:"storage"`
	UpdatedAt     int64                             `json:"updatedAt"`
}

func computeConfigHash(cfg resolvedConfig) string {
	payload := map[string]any{
		"schemaVersion": schemaVersion,
		"agentKey":      cfg.AgentKey,
		"workspaceRoot": cfg.WorkspaceRoot,
		"storage":       cfg.Storage,
		"embedding":     cfg.Embedding,
		"include":       cfg.Include,
		"exclude":       cfg.Exclude,
		"chunk":         cfg.Chunk,
		"retrieval":     cfg.Retrieval,
		"extraction":    cfg.Extraction,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writeManifest(storageDir string, cfg resolvedConfig) error {
	data, err := json.MarshalIndent(manifest{
		SchemaVersion: schemaVersion,
		AgentKey:      cfg.AgentKey,
		WorkspaceRoot: cfg.WorkspaceRoot,
		ConfigHash:    cfg.ConfigHash,
		Embedding:     cfg.Embedding,
		Include:       append([]string(nil), cfg.Include...),
		Exclude:       append([]string(nil), cfg.Exclude...),
		Chunk:         cfg.Chunk,
		Retrieval:     cfg.Retrieval,
		Extraction:    cfg.Extraction,
		Storage:       cfg.Storage,
		UpdatedAt:     time.Now().UnixMilli(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(storageDir, "manifest.json"), data, 0o644)
}

func indexWorkspace(ctx context.Context, store *Store, cfg resolvedConfig, embedder *Embedder, force bool, run *IndexRun) error {
	previousHash := store.Meta("configHash")
	if force || previousHash != "" && previousHash != cfg.ConfigHash {
		if err := store.ClearIndex(); err != nil {
			return err
		}
		force = true
	}
	seen := map[string]struct{}{}
	includeMatchers := compileMatchers(cfg.Include)
	excludeMatchers := compileMatchers(append(defaultExcludes(), cfg.Exclude...))

	err := filepath.WalkDir(cfg.WorkspaceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == cfg.WorkspaceRoot {
			return nil
		}
		rel, err := filepath.Rel(cfg.WorkspaceRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if matchesAny(excludeMatchers, rel+"/") || shouldSkipDirName(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if matchesAny(excludeMatchers, rel) {
			return nil
		}
		if len(includeMatchers) > 0 && !matchesAny(includeMatchers, rel) {
			return nil
		}
		run.ScannedFiles++
		seen[rel] = struct{}{}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if err := indexOneFile(ctx, store, cfg, embedder, path, rel, info, force, run); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	active, err := store.ActiveFilePaths()
	if err != nil {
		return err
	}
	for path := range active {
		if _, ok := seen[path]; ok {
			continue
		}
		if err := store.MarkDeleted(path); err != nil {
			return err
		}
		run.DeletedFiles++
	}
	if err := store.SetMeta("schemaVersion", schemaVersion); err != nil {
		return err
	}
	if err := store.SetMeta("agentKey", cfg.AgentKey); err != nil {
		return err
	}
	if err := store.SetMeta("workspaceRoot", cfg.WorkspaceRoot); err != nil {
		return err
	}
	if err := store.SetMeta("configHash", cfg.ConfigHash); err != nil {
		return err
	}
	if err := store.SetMeta("embeddingModelKey", cfg.Embedding.ModelKey); err != nil {
		return err
	}
	if err := store.SetMeta("embeddingProviderKey", cfg.Embedding.ProviderKey); err != nil {
		return err
	}
	if err := store.SetMeta("embeddingModel", cfg.Embedding.Model); err != nil {
		return err
	}
	if err := store.SetMeta("embeddingDimension", fmt.Sprintf("%d", cfg.Embedding.Dimension)); err != nil {
		return err
	}
	if err := store.SetMeta("lastIndexedAt", fmt.Sprintf("%d", time.Now().UnixMilli())); err != nil {
		return err
	}
	return writeManifest(cfg.StorageDir, cfg)
}

func indexOneFile(ctx context.Context, store *Store, cfg resolvedConfig, embedder *Embedder, fullPath string, rel string, info fs.FileInfo, force bool, run *IndexRun) error {
	ext := strings.ToLower(filepath.Ext(rel))
	rec := fileRecord{
		ID:        fileID(rel),
		Path:      rel,
		Ext:       ext,
		Size:      info.Size(),
		MTimeMS:   info.ModTime().UnixMilli(),
		Mime:      mimeForExtension(ext),
		Extractor: extractorNameForExtension(ext, cfg.Extraction),
		Status:    "active",
		IndexedAt: time.Now().UnixMilli(),
	}
	if rec.Extractor == "" {
		rec.Status = "skipped"
		rec.SkipReason = "unsupported_extension"
		return store.UpsertSkippedFile(rec)
	}
	if info.Size() > extractionMaxFileBytes(cfg.Extraction) {
		rec.Status = "skipped"
		rec.SkipReason = "file_too_large"
		return store.UpsertSkippedFile(rec)
	}
	existing, err := store.File(rel)
	if err != nil {
		return err
	}
	if !force && existing != nil && existing.Status == "active" && existing.Size == rec.Size && existing.MTimeMS == rec.MTimeMS {
		return nil
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		rec.Status = "error"
		rec.Error = err.Error()
		return store.UpsertSkippedFile(rec)
	}
	rec.SHA256 = shaHex(data)
	if !force && existing != nil && existing.Status == "active" && existing.SHA256 == rec.SHA256 {
		return nil
	}
	if _, ok := supportedTextExtensions[ext]; ok && (!utf8.Valid(data) || looksBinary(data)) {
		rec.Status = "skipped"
		rec.SkipReason = "binary_or_non_utf8"
		return store.UpsertSkippedFile(rec)
	}
	doc, err := extractDocument(ctx, fullPath, rel, ext, data, cfg.Extraction)
	if err != nil {
		var exErr extractionError
		if errors.As(err, &exErr) && exErr.skipped {
			rec.Status = "skipped"
			rec.SkipReason = exErr.reason
			rec.Error = exErr.message
			return store.UpsertSkippedFile(rec)
		}
		rec.Status = "error"
		rec.Error = err.Error()
		return store.UpsertSkippedFile(rec)
	}
	rec.Mime = firstNonBlank(doc.Mime, rec.Mime)
	rec.Extractor = firstNonBlank(doc.Extractor, rec.Extractor)
	rec.Metadata = metadataJSON(doc.Metadata)
	rec.TextSHA256 = shaHex([]byte(extractedText(doc)))
	chunks := chunkExtractedDocument(rel, doc, cfg.Chunk.MaxChars, cfg.Chunk.OverlapChars, cfg.Embedding.Model, cfg.Embedding.Dimension)
	if len(chunks) > 0 {
		texts := make([]string, len(chunks))
		for i := range chunks {
			texts[i] = chunks[i].Content
		}
		vectors, err := embedder.Embed(ctx, texts)
		if err != nil {
			return err
		}
		for i := range chunks {
			chunks[i].Embedding = vectors[i]
		}
	}
	if err := store.UpsertIndexedFile(rec, chunks); err != nil {
		return err
	}
	run.ChangedFiles++
	run.IndexedChunks += len(chunks)
	return nil
}

func chunkText(path string, text string, maxChars int, overlapChars int, embeddingModel string, embeddingDimension int) []chunkRecord {
	lineCount := countLines(text)
	return chunkExtractedDocument(path, extractedDocument{Blocks: []extractedBlock{{
		SourceType: "text",
		Content:    text,
		StartLine:  1,
		EndLine:    lineCount,
	}}}, maxChars, overlapChars, embeddingModel, embeddingDimension)
}

func chunkExtractedDocument(path string, doc extractedDocument, maxChars int, overlapChars int, embeddingModel string, embeddingDimension int) []chunkRecord {
	if maxChars <= 0 {
		maxChars = 4000
	}
	if overlapChars < 0 {
		overlapChars = 0
	}
	if overlapChars >= maxChars {
		overlapChars = maxChars / 5
	}
	type block struct {
		content    string
		startLine  int
		endLine    int
		heading    string
		sourceType string
		pageStart  int
		pageEnd    int
		slideStart int
		slideEnd   int
	}
	blocks := []block{}
	for _, source := range doc.Blocks {
		content := strings.TrimSpace(source.Content)
		if content == "" {
			continue
		}
		lines := strings.Split(content, "\n")
		var current strings.Builder
		sourceStartLine := source.StartLine
		if sourceStartLine <= 0 {
			sourceStartLine = 1
		}
		startLine := sourceStartLine
		heading := source.Heading
		for i, line := range lines {
			lineNo := i + 1
			absoluteLine := sourceStartLine + lineNo - 1
			if h := markdownHeading(line); h != "" {
				heading = h
			}
			if current.Len() == 0 {
				startLine = absoluteLine
			}
			if current.Len()+len(line)+1 > maxChars && current.Len() > 0 {
				blocks = append(blocks, block{
					content:    strings.TrimSpace(current.String()),
					startLine:  startLine,
					endLine:    absoluteLine - 1,
					heading:    heading,
					sourceType: source.SourceType,
					pageStart:  source.PageStart,
					pageEnd:    source.PageEnd,
					slideStart: source.SlideStart,
					slideEnd:   source.SlideEnd,
				})
				current.Reset()
				if overlapChars > 0 && len(blocks[len(blocks)-1].content) > overlapChars {
					overlap := tailByChars(blocks[len(blocks)-1].content, overlapChars)
					current.WriteString(overlap)
					current.WriteByte('\n')
				}
				startLine = absoluteLine
			}
			current.WriteString(line)
			current.WriteByte('\n')
		}
		if strings.TrimSpace(current.String()) != "" {
			endLine := sourceStartLine + len(lines) - 1
			if source.EndLine > 0 {
				endLine = minInt(endLine, source.EndLine)
			}
			blocks = append(blocks, block{
				content:    strings.TrimSpace(current.String()),
				startLine:  startLine,
				endLine:    endLine,
				heading:    heading,
				sourceType: source.SourceType,
				pageStart:  source.PageStart,
				pageEnd:    source.PageEnd,
				slideStart: source.SlideStart,
				slideEnd:   source.SlideEnd,
			})
		}
	}
	out := make([]chunkRecord, 0, len(blocks))
	now := time.Now().UnixMilli()
	for i, block := range blocks {
		hash := shaHex([]byte(block.content))
		out = append(out, chunkRecord{
			ID:                 chunkID(path, i, hash),
			Path:               path,
			Ordinal:            i,
			Heading:            block.heading,
			StartLine:          block.startLine,
			EndLine:            block.endLine,
			SourceType:         block.sourceType,
			PageStart:          block.pageStart,
			PageEnd:            block.pageEnd,
			SlideStart:         block.slideStart,
			SlideEnd:           block.slideEnd,
			Content:            block.content,
			ContentHash:        hash,
			EmbeddingModel:     embeddingModel,
			EmbeddingDimension: embeddingDimension,
			UpdatedAt:          now,
		})
		out[len(out)-1].LocatorJSON = chunkLocatorJSON(out[len(out)-1])
	}
	return out
}

func extractedText(doc extractedDocument) string {
	parts := make([]string, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		if text := strings.TrimSpace(block.Content); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func metadataJSON(metadata map[string]any) string {
	if len(metadata) == 0 {
		return ""
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return ""
	}
	return string(data)
}

func chunkLocatorJSON(chunk chunkRecord) string {
	locator := map[string]any{
		"sourceType": chunk.SourceType,
		"startLine":  chunk.StartLine,
		"endLine":    chunk.EndLine,
	}
	if chunk.PageStart > 0 {
		locator["pageStart"] = chunk.PageStart
		locator["pageEnd"] = chunk.PageEnd
	}
	if chunk.SlideStart > 0 {
		locator["slideStart"] = chunk.SlideStart
		locator["slideEnd"] = chunk.SlideEnd
	}
	data, err := json.Marshal(locator)
	if err != nil {
		return ""
	}
	return string(data)
}

func markdownHeading(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return ""
	}
	count := 0
	for _, r := range trimmed {
		if r != '#' {
			break
		}
		count++
	}
	if count == 0 || count > 6 || len(trimmed) <= count || trimmed[count] != ' ' {
		return ""
	}
	return strings.TrimSpace(trimmed[count:])
}

func tailByChars(text string, maxChars int) string {
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return string(runes[len(runes)-maxChars:])
}

func shaHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fileID(path string) string {
	return "kbf_" + shaHex([]byte(path))[:24]
}

func chunkID(path string, ordinal int, contentHash string) string {
	return "kbc_" + shaHex([]byte(fmt.Sprintf("%s:%d:%s", path, ordinal, contentHash)))[:24]
}

func looksBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for _, b := range data[:limit] {
		if b == 0 {
			return true
		}
	}
	return false
}

func shouldSkipDirName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", ".kbase", "node_modules":
		return true
	default:
		return false
	}
}

func defaultExcludes() []string {
	return []string{".git/**", ".kbase/**", "node_modules/**"}
}

type matcher struct {
	pattern string
	re      *regexp.Regexp
}

func compileMatchers(patterns []string) []matcher {
	out := []matcher{}
	seen := map[string]struct{}{}
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		out = append(out, matcher{pattern: pattern, re: regexp.MustCompile(globToRegexp(pattern))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pattern < out[j].pattern })
	return out
}

func matchesAny(matchers []matcher, path string) bool {
	path = filepath.ToSlash(strings.TrimPrefix(path, "./"))
	for _, matcher := range matchers {
		if matcher.re.MatchString(path) {
			return true
		}
	}
	return false
}

func globToRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		if ch == '*' {
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
				continue
			}
			b.WriteString(`[^/]*`)
			continue
		}
		if ch == '?' {
			b.WriteString(`[^/]`)
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(ch)))
	}
	b.WriteString("$")
	return b.String()
}
