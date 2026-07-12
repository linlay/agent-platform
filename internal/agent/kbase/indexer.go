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

	"agent-platform/internal/supportpkg"
)

const (
	schemaVersion       = "2"
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
	Support       *supportpkg.Registry
	FTSTokenizer  string
	IndexHash     string
	QueryHash     string
	// ConfigHash is the legacy name for IndexHash. Keep populating it for one
	// compatibility cycle so older status and manifest readers remain valid.
	ConfigHash string
}

type manifest struct {
	SchemaVersion string            `json:"schemaVersion"`
	AgentKey      string            `json:"agentKey"`
	WorkspaceRoot string            `json:"workspaceRoot"`
	ConfigHash    string            `json:"configHash"`
	IndexHash     string            `json:"indexHash,omitempty"`
	QueryHash     string            `json:"queryHash,omitempty"`
	Embedding     EmbeddingSnapshot `json:"embedding"`
	Include       []string          `json:"include"`
	Exclude       []string          `json:"exclude"`
	Chunk         ChunkConfig       `json:"chunk"`
	Retrieval     RetrievalConfig   `json:"retrieval"`
	Extraction    ExtractionConfig  `json:"extraction"`
	Storage       string            `json:"storage"`
	FTSTokenizer  string            `json:"ftsTokenizer,omitempty"`
	UpdatedAt     int64             `json:"updatedAt"`
}

// computeConfigHash is retained as a compatibility alias. A config hash has
// always meant "does this index need to be rebuilt" to its callers, so it now
// deliberately excludes query-time retrieval tuning.
func computeConfigHash(cfg resolvedConfig) string {
	return computeIndexHash(cfg)
}

func computeIndexHash(cfg resolvedConfig) string {
	return computeIndexHashForSchema(cfg, schemaVersion)
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
	if value := strings.TrimSpace(cfg.ConfigHash); value != "" {
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

// storedIndexHash upgrades schema-v2 stores without forcing a rebuild merely
// because the old configHash also included retrieval weights. The old manifest
// contains all index-affecting settings, so derive the new index hash from it.
func storedIndexHash(store workspaceIndexStore, storageDir string) string {
	if value := strings.TrimSpace(store.Meta("indexHash")); value != "" {
		return value
	}
	legacyHash := strings.TrimSpace(store.Meta("configHash"))
	if legacyHash == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(storageDir, "manifest.json"))
	if err != nil {
		return legacyHash
	}
	var previous manifest
	if err := json.Unmarshal(data, &previous); err != nil {
		return legacyHash
	}
	if value := strings.TrimSpace(previous.IndexHash); value != "" {
		return value
	}
	version := strings.TrimSpace(previous.SchemaVersion)
	if version == "" {
		version = schemaVersion
	}
	return computeIndexHashForSchema(resolvedConfig{
		WorkspaceRoot: previous.WorkspaceRoot,
		Storage:       previous.Storage,
		Embedding:     previous.Embedding,
		Include:       previous.Include,
		Exclude:       previous.Exclude,
		Chunk:         previous.Chunk,
		Extraction:    previous.Extraction,
		FTSTokenizer:  previous.FTSTokenizer,
	}, version)
}

func writeManifest(storageDir string, cfg resolvedConfig) error {
	indexHash := desiredIndexHash(cfg)
	queryHash := desiredQueryHash(cfg)
	tokenizer := strings.ToLower(strings.TrimSpace(cfg.FTSTokenizer))
	if tokenizer == "" {
		tokenizer = defaultFTSTokenizer
	}
	data, err := json.MarshalIndent(manifest{
		SchemaVersion: schemaVersion,
		AgentKey:      cfg.AgentKey,
		WorkspaceRoot: cfg.WorkspaceRoot,
		ConfigHash:    indexHash,
		IndexHash:     indexHash,
		QueryHash:     queryHash,
		Embedding:     cfg.Embedding,
		Include:       append([]string(nil), cfg.Include...),
		Exclude:       append([]string(nil), cfg.Exclude...),
		Chunk:         cfg.Chunk,
		Retrieval:     cfg.Retrieval,
		Extraction:    cfg.Extraction,
		Storage:       cfg.Storage,
		FTSTokenizer:  tokenizer,
		UpdatedAt:     time.Now().UnixMilli(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(storageDir, "manifest.json"), data, 0o644)
}

func indexWorkspace(ctx context.Context, store workspaceIndexStore, cfg resolvedConfig, embedder *Embedder, force bool, run *IndexRun) error {
	indexHash := desiredIndexHash(cfg)
	queryHash := desiredQueryHash(cfg)
	previousHash := storedIndexHash(store, cfg.StorageDir)
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
	return writeManifest(cfg.StorageDir, cfg)
}

func indexOneFile(ctx context.Context, store workspaceIndexStore, cfg resolvedConfig, embedder *Embedder, fullPath string, rel string, info fs.FileInfo, force bool, run *IndexRun) error {
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
	if !force && existing != nil && existing.Status == "error" &&
		strings.HasPrefix(existing.Error, "recovery failed three times:") &&
		existing.Size == rec.Size && existing.MTimeMS == rec.MTimeMS {
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
	doc, err := extractDocument(ctx, fullPath, rel, ext, data, cfg.Extraction, cfg.Support)
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
	chunks := chunkExtractedDocument(rel, doc, cfg.Chunk, cfg.Embedding.Model, cfg.Embedding.Dimension)
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
