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
)

const (
	defaultMaxFileBytes = 50 * 1024 * 1024
	defaultFTSTokenizer = "icu"
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
	Chunk         ChunkConfig
	Retrieval     RetrievalConfig
	Extraction    ExtractionConfig
	FTSTokenizer  string
	IndexHash     string
	QueryHash     string
}

// computeConfigHash is retained as a compatibility alias. A config hash has
// always meant "does this index need to be rebuilt" to its callers, so it now
// deliberately excludes query-time retrieval tuning.
func computeConfigHash(cfg resolvedConfig) string {
	return computeIndexHash(cfg)
}

func computeIndexHash(cfg resolvedConfig) string {
	return computeIndexHashForSchema(cfg, IndexSchemaVersion)
}

func computeIndexHashForSchema(cfg resolvedConfig, version string) string {
	tokenizer := strings.ToLower(strings.TrimSpace(cfg.FTSTokenizer))
	if tokenizer == "" {
		tokenizer = defaultFTSTokenizer
	}
	payload := map[string]any{
		"schemaVersion": version,
		"workspaceRoot": cfg.WorkspaceRoot,
		"storage":       cfg.Storage,
		"embedding": map[string]any{
			"modelKey":  cfg.Embedding.ModelKey,
			"model":     cfg.Embedding.Model,
			"dimension": cfg.Embedding.Dimension,
		},
		"include":      cfg.Include,
		"exclude":      cfg.Exclude,
		"chunk":        cfg.Chunk,
		"extraction":   cfg.Extraction,
		"ftsTokenizer": tokenizer,
	}
	return hashConfigPayload(payload)
}

func computeQueryHash(cfg resolvedConfig) string {
	payload := map[string]any{
		"topK":                cfg.Retrieval.TopK,
		"fusion":              cfg.Retrieval.Fusion,
		"rrfK":                cfg.Retrieval.RRFK,
		"vectorWeight":        cfg.Retrieval.VectorWeight,
		"ftsWeight":           cfg.Retrieval.FTSWeight,
		"candidateFloor":      cfg.Retrieval.CandidateFloor,
		"candidateMultiplier": cfg.Retrieval.CandidateMultiplier,
		"candidateMax":        cfg.Retrieval.CandidateMax,
	}
	return hashConfigPayload(payload)
}

func hashConfigPayload(payload any) string {
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func desiredIndexHash(cfg resolvedConfig) string {
	if value := strings.TrimSpace(cfg.IndexHash); value != "" {
		return value
	}
	return computeIndexHash(cfg)
}

func desiredQueryHash(cfg resolvedConfig) string {
	if value := strings.TrimSpace(cfg.QueryHash); value != "" {
		return value
	}
	return computeQueryHash(cfg)
}

func indexWorkspace(ctx context.Context, store workspaceIndexStore, cfg resolvedConfig, embedder *Embedder, force bool, run *IndexRun) error {
	indexHash := desiredIndexHash(cfg)
	queryHash := desiredQueryHash(cfg)
	previousHash := strings.TrimSpace(store.Meta("indexHash"))
	if force || previousHash != "" && previousHash != indexHash {
		if err := store.ClearIndex(); err != nil {
			return err
		}
		force = true
	}
	seen := map[string]struct{}{}
	includeMatchers := compileMatchers(cfg.Include)
	excludeMatchers := compileMatchers(append(DefaultExcludePatterns(), cfg.Exclude...))

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
		if err := indexOneFile(ctx, store, cfg, embedder, path, rel, info, force, false, run); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	active, err := store.TrackedFilePaths()
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
	if err := store.SetMeta("schemaVersion", ControlSchemaVersion); err != nil {
		return err
	}
	if err := store.SetMeta("agentKey", cfg.AgentKey); err != nil {
		return err
	}
	if err := store.SetMeta("workspaceRoot", cfg.WorkspaceRoot); err != nil {
		return err
	}
	if err := store.SetMeta("indexHash", indexHash); err != nil {
		return err
	}
	if err := store.SetMeta("queryHash", queryHash); err != nil {
		return err
	}
	if err := store.SetMeta("configHash", indexHash); err != nil {
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
	return nil
}

// indexWorkspacePaths applies a watcher change set without walking unrelated
// workspace paths. Events are hints only: the filesystem is re-checked while
// holding the per-storage refresh lock.
func indexWorkspacePaths(ctx context.Context, store workspaceIndexStore, cfg resolvedConfig, embedder *Embedder, paths []string, run *IndexRun) error {
	paths = compactChangedPaths(paths)
	run.CandidatePaths = len(paths)
	includeMatchers := compileMatchers(cfg.Include)
	excludeMatchers := compileMatchers(append(DefaultExcludePatterns(), cfg.Exclude...))
	processed := map[string]struct{}{}
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel = normalizeIndexedPath(rel)
		if rel == "" {
			return indexWorkspace(ctx, store, cfg, embedder, false, run)
		}
		fullPath := filepath.Join(cfg.WorkspaceRoot, filepath.FromSlash(rel))
		info, err := os.Stat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				if err := deleteTrackedPrefix(store, rel, run); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if info.IsDir() {
			seen := map[string]struct{}{}
			err := filepath.WalkDir(fullPath, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return nil
				}
				child, relErr := filepath.Rel(cfg.WorkspaceRoot, path)
				if relErr != nil {
					return nil
				}
				child = filepath.ToSlash(child)
				if entry.IsDir() {
					if child != rel && (matchesAny(excludeMatchers, child+"/") || shouldSkipDirName(entry.Name())) {
						return filepath.SkipDir
					}
					return nil
				}
				if !shouldIndexPath(child, includeMatchers, excludeMatchers) {
					return nil
				}
				seen[child] = struct{}{}
				if _, ok := processed[child]; ok {
					return nil
				}
				processed[child] = struct{}{}
				fileInfo, infoErr := entry.Info()
				if infoErr != nil {
					return nil
				}
				run.ScannedFiles++
				return indexOneFile(ctx, store, cfg, embedder, path, child, fileInfo, false, true, run)
			})
			if err != nil {
				return err
			}
			if err := deleteTrackedPrefixExcept(store, rel, seen, run); err != nil {
				return err
			}
			continue
		}
		if !shouldIndexPath(rel, includeMatchers, excludeMatchers) {
			if err := deleteTrackedPrefix(store, rel, run); err != nil {
				return err
			}
			continue
		}
		if _, ok := processed[rel]; ok {
			continue
		}
		processed[rel] = struct{}{}
		run.ScannedFiles++
		if err := indexOneFile(ctx, store, cfg, embedder, fullPath, rel, info, false, true, run); err != nil {
			return err
		}
	}
	return store.SetMeta("lastIndexedAt", fmt.Sprintf("%d", time.Now().UnixMilli()))
}

func shouldIndexPath(path string, includeMatchers, excludeMatchers []matcher) bool {
	if matchesAny(excludeMatchers, path) {
		return false
	}
	return len(includeMatchers) == 0 || matchesAny(includeMatchers, path)
}

func compactChangedPaths(paths []string) []string {
	set := map[string]struct{}{}
	for _, path := range paths {
		path = normalizeIndexedPath(path)
		if path == "." {
			path = ""
		}
		set[path] = struct{}{}
	}
	ordered := make([]string, 0, len(set))
	for path := range set {
		ordered = append(ordered, path)
	}
	sort.Slice(ordered, func(i, j int) bool {
		leftDepth, rightDepth := strings.Count(ordered[i], "/"), strings.Count(ordered[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return ordered[i] < ordered[j]
	})
	out := make([]string, 0, len(ordered))
	for _, path := range ordered {
		covered := false
		for _, parent := range out {
			if parent == "" || strings.HasPrefix(path, parent+"/") {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, path)
		}
	}
	return out
}

func deleteTrackedPrefix(store workspaceIndexStore, prefix string, run *IndexRun) error {
	return deleteTrackedPrefixExcept(store, prefix, nil, run)
}

func deleteTrackedPrefixExcept(store workspaceIndexStore, prefix string, seen map[string]struct{}, run *IndexRun) error {
	tracked, err := store.TrackedFilePaths()
	if err != nil {
		return err
	}
	for path := range tracked {
		if path != prefix && !strings.HasPrefix(path, strings.TrimSuffix(prefix, "/")+"/") {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		if err := store.MarkDeleted(path); err != nil {
			return err
		}
		run.DeletedFiles++
	}
	return nil
}

func indexOneFile(ctx context.Context, store workspaceIndexStore, cfg resolvedConfig, embedder *Embedder, fullPath string, rel string, info fs.FileInfo, force, verifyContent bool, run *IndexRun) error {
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
	existing, err := store.File(rel)
	if err != nil {
		return err
	}
	if !force && !verifyContent && existing != nil && existing.Status != "deleted" && existing.Size == rec.Size && existing.MTimeMS == rec.MTimeMS {
		if existing.Status != "error" || !strings.HasPrefix(existing.Error, "recovery failed three times:") {
			run.UnchangedFiles++
			return nil
		}
		return nil
	}
	if rec.Extractor == "" {
		rec.Status = "skipped"
		rec.SkipReason = "unsupported_extension"
		return commitSkippedFile(store, rec, existing, run)
	}
	if info.Size() > extractionMaxFileBytes(cfg.Extraction) {
		rec.Status = "skipped"
		rec.SkipReason = "file_too_large"
		return commitSkippedFile(store, rec, existing, run)
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		rec.Status = "error"
		rec.Error = err.Error()
		return commitSkippedFile(store, rec, existing, run)
	}
	rec.SHA256 = shaHex(data)
	if !force && existing != nil && existing.Status != "deleted" && existing.SHA256 != "" && existing.SHA256 == rec.SHA256 {
		preserveIndexedRecord(&rec, *existing)
		if err := store.UpsertMetadataFile(rec); err != nil {
			return err
		}
		run.MetadataOnlyFiles++
		return nil
	}
	if _, ok := supportedTextExtensions[ext]; ok && (!utf8.Valid(data) || looksBinary(data)) {
		rec.Status = "skipped"
		rec.SkipReason = "binary_or_non_utf8"
		return commitSkippedFile(store, rec, existing, run)
	}
	doc, err := extractDocument(ctx, fullPath, rel, ext, data, cfg.Extraction)
	if err != nil {
		var exErr extractionError
		if errors.As(err, &exErr) && exErr.skipped {
			rec.Status = "skipped"
			rec.SkipReason = exErr.reason
			rec.Error = exErr.message
			return commitSkippedFile(store, rec, existing, run)
		}
		rec.Status = "error"
		rec.Error = err.Error()
		return commitSkippedFile(store, rec, existing, run)
	}
	rec.Mime = firstNonBlank(doc.Mime, rec.Mime)
	rec.Extractor = firstNonBlank(doc.Extractor, rec.Extractor)
	rec.Metadata = metadataJSON(doc.Metadata)
	rec.TextSHA256 = shaHex([]byte(extractedText(doc)))
	chunks := chunkExtractedDocument(rel, doc, cfg.Chunk, cfg.Embedding.Model, cfg.Embedding.Dimension)
	for i := range chunks {
		chunks[i].FileID = rec.ID
	}
	desiredChunkSetHash := chunkValidationSetHash(chunks)
	if !force && existing != nil && existing.Status == "active" && existing.ChunkSetHash != "" && existing.ChunkSetHash == desiredChunkSetHash {
		rec.ChunkCount = existing.ChunkCount
		rec.ChunkSetHash = existing.ChunkSetHash
		if err := store.UpsertMetadataFile(rec); err != nil {
			return err
		}
		run.MetadataOnlyFiles++
		return nil
	}
	if len(chunks) > 0 {
		cached := map[string][]float64{}
		if !force && existing != nil && existing.Status == "active" {
			cached, err = store.FileEmbeddings(existing.ID, cfg.Embedding.Model, cfg.Embedding.Dimension)
			if err != nil {
				return err
			}
		}
		missing := make([]int, 0, len(chunks))
		texts := make([]string, 0, len(chunks))
		for i := range chunks {
			if vector := cached[chunks[i].ContentHash]; len(vector) == cfg.Embedding.Dimension {
				chunks[i].Embedding = append([]float64(nil), vector...)
				run.ReusedChunks++
				continue
			}
			missing = append(missing, i)
			texts = append(texts, chunks[i].Content)
		}
		if len(texts) > 0 {
			vectors, embedErr := embedder.Embed(ctx, texts)
			if embedErr != nil {
				return embedErr
			}
			for i, chunkIndex := range missing {
				chunks[chunkIndex].Embedding = vectors[i]
			}
			run.EmbeddedChunks += len(texts)
		}
	}
	if err := store.UpsertIndexedFile(rec, chunks); err != nil {
		return err
	}
	run.ChangedFiles++
	if existing == nil || existing.Status == "deleted" {
		run.NewFiles++
	} else {
		run.ModifiedFiles++
	}
	run.IndexedChunks += len(chunks)
	return nil
}

func preserveIndexedRecord(rec *fileRecord, existing fileRecord) {
	rec.TextSHA256 = existing.TextSHA256
	rec.Extractor = existing.Extractor
	rec.Metadata = existing.Metadata
	rec.Status = existing.Status
	rec.SkipReason = existing.SkipReason
	rec.Error = existing.Error
	rec.ChunkCount = existing.ChunkCount
	rec.ChunkSetHash = existing.ChunkSetHash
	rec.IndexedAt = existing.IndexedAt
	rec.DeletedAt = 0
}

func commitSkippedFile(store workspaceIndexStore, rec fileRecord, existing *fileRecord, run *IndexRun) error {
	if existing != nil && existing.Status == rec.Status && existing.SkipReason == rec.SkipReason && existing.Error == rec.Error && existing.ChunkCount == 0 {
		rec.SHA256 = existing.SHA256
		rec.TextSHA256 = existing.TextSHA256
		rec.Metadata = existing.Metadata
		if err := store.UpsertMetadataFile(rec); err != nil {
			return err
		}
		run.MetadataOnlyFiles++
		return nil
	}
	if err := store.UpsertSkippedFile(rec); err != nil {
		return err
	}
	run.ChangedFiles++
	if existing == nil || existing.Status == "deleted" {
		run.NewFiles++
	} else {
		run.ModifiedFiles++
	}
	return nil
}

func chunkText(path string, text string, chunkCfg ChunkConfig, embeddingModel string, embeddingDimension int) []chunkRecord {
	lineCount := countLines(text)
	return chunkExtractedDocument(path, extractedDocument{Blocks: []extractedBlock{{
		SourceType: "text",
		Content:    text,
		StartLine:  1,
		EndLine:    lineCount,
	}}}, chunkCfg, embeddingModel, embeddingDimension)
}

func chunkExtractedDocument(path string, doc extractedDocument, chunkCfg ChunkConfig, embeddingModel string, embeddingDimension int) []chunkRecord {
	budget := resolveChunkBudget(chunkCfg)
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
		currentIsOverlap := false
		sourceStartLine := source.StartLine
		if sourceStartLine <= 0 {
			sourceStartLine = 1
		}
		startLine := sourceStartLine
		heading := source.Heading
		appendCurrent := func(endLine int) {
			text := strings.TrimSpace(current.String())
			current.Reset()
			currentIsOverlap = false
			if text == "" {
				return
			}
			blocks = append(blocks, block{
				content:    text,
				startLine:  startLine,
				endLine:    endLine,
				heading:    heading,
				sourceType: source.SourceType,
				pageStart:  source.PageStart,
				pageEnd:    source.PageEnd,
				slideStart: source.SlideStart,
				slideEnd:   source.SlideEnd,
			})
			if budget.overlap > 0 && measureChunkText(text, budget.unit) > budget.overlap {
				overlap := tailByBudget(text, budget)
				if strings.TrimSpace(overlap) != "" {
					current.WriteString(overlap)
					current.WriteByte('\n')
					currentIsOverlap = true
				}
			}
		}
		for i, line := range lines {
			lineNo := i + 1
			absoluteLine := sourceStartLine + lineNo - 1
			if h := markdownHeading(line); h != "" {
				heading = h
			}
			pieces := splitLineByBudget(line, budget)
			for pieceIndex, piece := range pieces {
				segment := piece
				if pieceIndex == len(pieces)-1 {
					segment += "\n"
				}
				if current.Len() == 0 {
					startLine = absoluteLine
				}
				if strings.TrimSpace(current.String()) != "" && measureChunkText(current.String()+segment, budget.unit) > budget.max {
					if currentIsOverlap {
						current.Reset()
						currentIsOverlap = false
					} else {
						endLine := absoluteLine
						if pieceIndex == 0 {
							endLine = absoluteLine - 1
						}
						appendCurrent(endLine)
					}
					startLine = absoluteLine
					if strings.TrimSpace(current.String()) != "" && measureChunkText(current.String()+segment, budget.unit) > budget.max {
						current.Reset()
						currentIsOverlap = false
					}
				}
				if current.Len() == 0 {
					startLine = absoluteLine
				}
				current.WriteString(segment)
				currentIsOverlap = false
			}
		}
		if strings.TrimSpace(current.String()) != "" && !currentIsOverlap {
			endLine := sourceStartLine + len(lines) - 1
			if source.EndLine > 0 {
				endLine = minInt(endLine, source.EndLine)
			}
			appendCurrent(endLine)
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

type chunkBudget struct {
	unit    string
	max     int
	overlap int
}

func resolveChunkBudget(cfg ChunkConfig) chunkBudget {
	cfg = NormalizeChunkConfig(cfg)
	if cfg.Unit == ChunkUnitChars {
		return chunkBudget{
			unit:    ChunkUnitChars,
			max:     cfg.MaxChars,
			overlap: cfg.OverlapChars,
		}
	}
	return chunkBudget{
		unit:    ChunkUnitEstimatedTokens,
		max:     cfg.MaxTokens,
		overlap: cfg.OverlapTokens,
	}
}

func measureChunkText(text string, unit string) int {
	if unit == ChunkUnitChars {
		return len([]rune(text))
	}
	return estimateChunkTokens(text)
}

func splitLineByBudget(line string, budget chunkBudget) []string {
	if budget.max <= 0 || measureChunkText(line, budget.unit) <= budget.max {
		return []string{line}
	}
	switch budget.unit {
	case ChunkUnitChars:
		return splitRunesByCount(line, budget.max)
	default:
		return splitRunesByEstimatedTokens(line, budget.max)
	}
}

func splitRunesByCount(text string, maxRunes int) []string {
	runes := []rune(text)
	if maxRunes <= 0 || len(runes) <= maxRunes {
		return []string{text}
	}
	out := make([]string, 0, (len(runes)/maxRunes)+1)
	for start := 0; start < len(runes); start += maxRunes {
		end := start + maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[start:end]))
	}
	return out
}

func splitRunesByEstimatedTokens(text string, maxTokens int) []string {
	if maxTokens <= 0 {
		return []string{text}
	}
	out := []string{}
	var current strings.Builder
	state := estimatedTokenState{}
	for _, r := range text {
		nextState := state
		nextState.add(r)
		if current.Len() > 0 && nextState.tokens > maxTokens {
			out = append(out, current.String())
			current.Reset()
			state = estimatedTokenState{}
			state.add(r)
			current.WriteRune(r)
			continue
		}
		state = nextState
		current.WriteRune(r)
	}
	if current.Len() > 0 || len(out) == 0 {
		out = append(out, current.String())
	}
	return out
}

func tailByBudget(text string, budget chunkBudget) string {
	if budget.overlap <= 0 {
		return ""
	}
	if budget.unit == ChunkUnitChars {
		return tailByChars(text, budget.overlap)
	}
	return tailByEstimatedTokens(text, budget.overlap)
}

func tailByChars(text string, maxChars int) string {
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return string(runes[len(runes)-maxChars:])
}

func tailByEstimatedTokens(text string, maxTokens int) string {
	runes := []rune(text)
	if len(runes) == 0 || estimateChunkTokens(text) <= maxTokens {
		return text
	}
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high) / 2
		if estimateChunkTokens(string(runes[mid:])) <= maxTokens {
			high = mid
		} else {
			low = mid + 1
		}
	}
	return string(runes[low:])
}

type estimatedTokenState struct {
	tokens         int
	asciiWordRunes int
	sawContent     bool
}

func estimateChunkTokens(text string) int {
	state := estimatedTokenState{}
	for _, r := range text {
		state.add(r)
	}
	if state.tokens <= 0 && state.sawContent {
		return 1
	}
	return state.tokens
}

func (s *estimatedTokenState) add(r rune) {
	if isASCIILetterOrDigit(r) {
		s.sawContent = true
		s.asciiWordRunes++
		if s.asciiWordRunes%4 == 1 {
			s.tokens++
		}
		return
	}
	s.asciiWordRunes = 0
	if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
		return
	}
	s.sawContent = true
	s.tokens++
}

func isASCIILetterOrDigit(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
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
