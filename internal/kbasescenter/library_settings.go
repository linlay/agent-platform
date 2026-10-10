package kbasescenter

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
type ModelsConfig struct {
	Embedding *EmbeddingConfig `json:"embedding,omitempty"`
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
	if d.Models != nil && d.Models.Embedding != nil {
		e := d.Models.Embedding
		fmt.Fprintf(b, "models:\n  embedding:\n    modelKey: %s\n", quote(e.ModelKey))
		if e.Prompt != "" {
			fmt.Fprintf(b, "    prompt: %s\n", quote(e.Prompt))
		}
	}
}
