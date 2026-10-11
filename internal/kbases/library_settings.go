package kbases

import (
	"agent-platform/internal/knowledge"
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/text/encoding/htmlindex"
	"strings"
)

type EmbeddingConfig struct {
	ModelKey string `json:"modelKey"`
	Prompt   string `json:"prompt,omitempty"`
}
type QueryModelConfig struct {
	ModelKey string `json:"modelKey"`
}
type ModelsConfig struct {
	Reranker       *QueryModelConfig `json:"reranker,omitempty"`
	QueryExpansion *QueryModelConfig `json:"queryExpansion,omitempty"`
	Embedding      *EmbeddingConfig  `json:"embedding,omitempty"`
}

// ConfiguredEngine receives the library configuration captured for each call.
type ConfiguredEngine interface {
	UpdateLibrary(context.Context, string, Definition, map[string][]string) error
	ReadLibrary(context.Context, string, Definition, string, string, int, ...string) (json.RawMessage, error)
}

func libraryChunk(d Definition) knowledge.ChunkSettings {
	if d.Chunk != nil {
		return *d.Chunk
	}
	return knowledge.ChunkSettings{}
}
func (s *Service) effectiveDefinition(d Definition) Definition {
	d.Collections = append([]Collection(nil), d.Collections...)
	for i := range d.Collections {
		c, _ := knowledge.ResolveSourceChunk(libraryChunk(d), d.Collections[i].Chunk)
		d.Collections[i].Chunk = knowledge.ChunkSettingsFrom(c)
	}
	d.Collections = s.effectiveCollections(d.Collections)
	return d
}
func normalizeTextEncoding(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", nil
	}
	enc, err := htmlindex.Get(label)
	if err != nil || enc == nil {
		return "", fmt.Errorf("unsupported textEncoding %q", label)
	}
	name, err := htmlindex.Name(enc)
	if err != nil || name == "replacement" {
		return "", fmt.Errorf("unsupported textEncoding %q", label)
	}
	return name, nil
}
func validateLibrarySettings(d Definition) error {
	if d.Retrieval != nil {
		if err := knowledge.ValidateRetrieval(d.Retrieval.Apply(knowledge.DefaultConfig().Retrieval)); err != nil {
			return err
		}
	}
	if d.Models != nil {
		for _, m := range []*QueryModelConfig{d.Models.Reranker, d.Models.QueryExpansion} {
			if m != nil && (strings.TrimSpace(m.ModelKey) == "" || len(m.ModelKey) > 200) {
				return fmt.Errorf("query modelKey is required and must be at most 200 bytes")
			}
		}
	}

	if err := knowledge.ValidateChunkSettings(libraryChunk(d)); err != nil {
		return err
	}
	if _, err := knowledge.ResolveSourceChunk(libraryChunk(d), knowledge.ChunkSettings{}); err != nil {
		return err
	}
	for _, c := range d.Collections {
		if _, err := knowledge.ResolveSourceChunk(libraryChunk(d), c.Chunk); err != nil {
			return fmt.Errorf("collection %s: %w", c.Name, err)
		}
	}
	if _, err := normalizeTextEncoding(d.TextEncoding); err != nil {
		return err
	}
	if d.Models != nil && d.Models.Embedding != nil {
		e := d.Models.Embedding
		if strings.TrimSpace(e.ModelKey) == "" || len(e.ModelKey) > 200 {
			return fmt.Errorf("models.embedding.modelKey is required and must be at most 200 bytes")
		}
		switch e.Prompt {
		case "", "raw", "qwen3", "embeddinggemma":
		default:
			return fmt.Errorf("models.embedding.prompt must be raw, qwen3 or embeddinggemma")
		}
	}
	return nil
}
func applyLibrarySettings(d *Definition, in Input) error {
	if in.Retrieval != nil {
		copy := *in.Retrieval
		d.Retrieval = &copy
	}
	if in.Chunk != nil {
		copy := *in.Chunk
		d.Chunk = &copy
	}
	if in.TextEncoding != nil {
		d.TextEncoding = *in.TextEncoding
	}
	if in.Models != nil {
		copy := *in.Models
		d.Models = &copy
	}
	label, err := normalizeTextEncoding(d.TextEncoding)
	if err != nil {
		return err
	}
	d.TextEncoding = label
	return validateLibrarySettings(*d)
}
func writeChunkSettings(b *strings.Builder, indent string, c knowledge.ChunkSettings, quote func(string) string) {
	if c == (knowledge.ChunkSettings{}) {
		return
	}
	fmt.Fprintf(b, "%schunk:\n", indent)
	if c.Unit != "" {
		fmt.Fprintf(b, "%s  unit: %s\n", indent, quote(c.Unit))
	}
	if c.Strategy != "" {
		fmt.Fprintf(b, "%s  strategy: %s\n", indent, quote(c.Strategy))
	}
	if c.MaxChars != nil {
		fmt.Fprintf(b, "%s  maxChars: %d\n", indent, *c.MaxChars)
	}
	if c.OverlapChars != nil {
		fmt.Fprintf(b, "%s  overlapChars: %d\n", indent, *c.OverlapChars)
	}
}
func writeLibrarySettings(b *strings.Builder, d Definition, quote func(string) string) {
	if d.Chunk != nil {
		writeChunkSettings(b, "", *d.Chunk, quote)
	}
	if d.TextEncoding != "" {
		fmt.Fprintf(b, "textEncoding: %s\n", quote(d.TextEncoding))
	}
	if d.Retrieval != nil {
		raw, _ := json.Marshal(d.Retrieval)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		if len(fields) > 0 {
			b.WriteString("retrieval:\n")
			for _, key := range []string{"topK", "candidateFloor", "candidateMultiplier", "candidateMax", "minScore", "recencyWeight", "recencyHalfLifeDays", "rerank", "queryExpansion"} {
				if value, ok := fields[key]; ok {
					fmt.Fprintf(b, "  %s: %v\n", key, value)
				}
			}
		}
	}
	if d.Models != nil && (d.Models.Embedding != nil || d.Models.Reranker != nil || d.Models.QueryExpansion != nil) {
		b.WriteString("models:\n")
		if e := d.Models.Embedding; e != nil {
			fmt.Fprintf(b, "  embedding:\n    modelKey: %s\n", quote(e.ModelKey))
			if e.Prompt != "" {
				fmt.Fprintf(b, "    prompt: %s\n", quote(e.Prompt))
			}
		}
		for _, role := range []struct {
			name string
			cfg  *QueryModelConfig
		}{{"reranker", d.Models.Reranker}, {"queryExpansion", d.Models.QueryExpansion}} {
			if role.cfg != nil {
				fmt.Fprintf(b, "  %s:\n    modelKey: %s\n", role.name, quote(role.cfg.ModelKey))
			}
		}
	}
}
