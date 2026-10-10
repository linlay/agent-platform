package kbasescenter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/knowledge"
)

func TestLibrarySettingsRoundTripAndSourceFingerprint(t *testing.T) {
	s := newStorageService(t, testEngine{})
	max, overlap, zero := 900, 90, 0
	encoding := "GBK"
	d, err := s.Create(Input{Name: "settings", Chunk: &knowledge.ChunkSettings{Strategy: "structural", MaxChars: &max, OverlapChars: &overlap}, TextEncoding: &encoding, Models: &ModelsConfig{Embedding: &EmbeddingConfig{ModelKey: "embed", Prompt: "qwen3"}}, Collections: []Collection{{Name: "docs", SourcePath: t.TempDir(), Chunk: knowledge.ChunkSettings{OverlapChars: &zero}}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	effective := s.effectiveDefinition(got).Collections[0].Chunk
	if effective.Strategy != "structural" || *effective.MaxChars != 900 || *effective.OverlapChars != 0 || got.TextEncoding != "gbk" {
		t.Fatalf("inheritance: %+v %+v", got, effective)
	}
	fp := s.fingerprints(got)
	got, err = s.Edit(d.ID, Input{Name: "rename"})
	if err != nil || got.Chunk == nil || got.Models == nil || got.TextEncoding != "gbk" {
		t.Fatalf("omitted settings lost: %+v %v", got, err)
	}
	if fp != s.fingerprints(got) {
		t.Fatal("metadata altered fingerprint")
	}
	overlap = 180
	got.Chunk.OverlapChars = &overlap
	if fp != s.fingerprints(got) {
		t.Fatal("overridden default changed source fingerprint")
	}
	got.Collections[0].Chunk = knowledge.ChunkSettings{}
	if fp == s.fingerprints(got) {
		t.Fatal("effective chunk change omitted from fingerprint")
	}
	raw, err := os.ReadFile(filepath.Join(s.root, d.ID, "library.yml"))
	if err != nil || !strings.Contains(string(raw), "overlapChars: 0") {
		t.Fatalf("explicit zero lost: %s %v", raw, err)
	}
	empty := ""
	got, err = s.Edit(d.ID, Input{Name: "reset", Chunk: &knowledge.ChunkSettings{}, TextEncoding: &empty, Models: &ModelsConfig{}})
	if err != nil || got.TextEncoding != "" || got.Models != nil || got.Chunk != nil {
		t.Fatalf("reset: %+v %v", got, err)
	}
	if fp == s.fingerprints(got) {
		t.Fatal("encoding/default reset omitted from fingerprint")
	}
}

func TestLibrarySettingsStrictValidation(t *testing.T) {
	for _, fields := range []string{
		`"chunk":{"strategy":"future"}`, `"chunk":{"maxChars":0}`, `"chunk":{"overlapChars":-1}`, `"chunk":{"unit":"estimatedTokens"}`, `"chunk":{"maxTokens":1000}`, `"textEncoding":"unknown"`, `"textEncoding":"replacement"`, `"models":{"embedding":{"modelKey":""}}`, `"models":{"embedding":{"modelKey":"a","prompt":"unknown"}}`, `"models":{"reranker":{"modelKey":"a"}}`,
	} {
		t.Run(fields, func(t *testing.T) {
			var in Input
			decoder := json.NewDecoder(strings.NewReader(`{"name":"invalid",` + fields + `}`))
			decoder.DisallowUnknownFields()
			err := decoder.Decode(&in)
			if err == nil {
				in.Collections = []Collection{{Name: "docs", SourcePath: t.TempDir()}}
				_, err = newStorageService(t, testEngine{}).Create(in)
			}
			if err == nil {
				t.Fatal("accepted invalid settings")
			}
		})
	}
}
