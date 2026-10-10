package kbasescenter

import "context"

// indexFingerprints separates source scope from the vector contract. Display,
// query and Run metadata is deliberately absent. An unchanged source hash keeps
// its old encoding so upgrading alone never invalidates a completed index.
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

// VectorEngine is the separate maintenance boundary for future library-level
// embedding settings. Current CenterEngine uses deployment-wide configuration
// and does not implement this interface; its vector fingerprint stays empty.
// RebuildVectors must never mutate sources, chunks or the full-text index.
type VectorEngine interface {
	VectorFingerprint(Definition) string
	RebuildVectors(context.Context, string, Definition) error
}

func (s *Service) fingerprints(d Definition) indexFingerprints {
	fp := indexFingerprints{Source: s.fingerprint(d.Collections)}
	if engine, ok := s.engine.(VectorEngine); ok {
		fp.Vector = engine.VectorFingerprint(d)
	}
	return fp
}
