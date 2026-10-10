package kbases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// indexFingerprints separates source scope from the vector contract. Display,
// query and Run metadata is deliberately absent.
type indexFingerprints struct{ Source, Vector string }

type indexChange uint8

const (
	indexUnchanged indexChange = iota
	indexVectorsChanged
	indexSourcesChanged
)

func (next indexFingerprints) changeFrom(previous indexFingerprints) indexChange {
	if next.Source != previous.Source {
		return indexSourcesChanged
	}
	if next.Vector != previous.Vector {
		return indexVectorsChanged
	}
	return indexUnchanged
}

// VectorEngine is the separate maintenance boundary for library-level embedding.
// RebuildVectors must never mutate sources, chunks or the full-text index.
type VectorEngine interface {
	VectorFingerprint(Definition) string
	RebuildVectors(context.Context, string, Definition) error
}

func (s *Service) fingerprints(d Definition) indexFingerprints {
	effective := s.effectiveDefinition(d)
	fp := indexFingerprints{Source: scopeFingerprint(effective.Collections)}
	if d.TextEncoding != "" {
		sum := sha256.Sum256([]byte(fp.Source + "\x00" + d.TextEncoding))
		fp.Source = hex.EncodeToString(sum[:])
	}
	if engine, ok := s.engine.(VectorEngine); ok {
		fp.Vector = engine.VectorFingerprint(d)
	}
	return fp
}
