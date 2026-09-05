package kbase

func chunkText(path string, text string, chunkCfg ChunkConfig, embeddingModel string, embeddingDimension int) []chunkRecord {
	lineCount := countLines(text)
	return chunkExtractedDocument(path, extractedDocument{Blocks: []extractedBlock{{
		SourceType: "text",
		Content:    text,
		StartLine:  1,
		EndLine:    lineCount,
	}}}, chunkCfg, embeddingModel, embeddingDimension)
}
