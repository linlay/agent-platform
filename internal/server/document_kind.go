package server

import (
	"net/http"

	"agent-platform/internal/documentmeta"
)

const (
	headerDocumentKind     = "X-Document-Kind"
	headerDocumentRevision = "X-Document-Revision"

	documentKindHTML     = documentmeta.KindHTML
	documentKindImage    = documentmeta.KindImage
	documentKindMarkdown = documentmeta.KindMarkdown
	documentKindText     = documentmeta.KindText
	documentKindCode     = documentmeta.KindCode
	documentKindPDF      = documentmeta.KindPDF
	documentKindOffice   = documentmeta.KindOffice
	documentKindAudio    = documentmeta.KindAudio
	documentKindVideo    = documentmeta.KindVideo
	documentKindArchive  = documentmeta.KindArchive
	documentKindBinary   = documentmeta.KindBinary
)

func normalizeDocumentMIME(value string) string {
	return documentmeta.NormalizeMIME(value)
}

func documentSampleIsText(sample []byte, lookahead []byte, complete bool) bool {
	return documentmeta.SampleIsText(sample, lookahead, complete)
}

func classifyDocumentKind(name string, mimeType string, sample []byte, lookahead []byte, complete bool) string {
	return documentmeta.Classify(name, mimeType, sample, lookahead, complete)
}

func resolveDocumentMetadata(name string, mimeType string, sample []byte, lookahead []byte, complete bool) documentmeta.Metadata {
	return documentmeta.Resolve(name, mimeType, sample, lookahead, complete)
}

func detectDocumentSampleMIME(name string, sample []byte, lookahead []byte, complete bool) string {
	return documentmeta.DetectMIME(name, sample, lookahead, complete)
}

func documentKindEditable(kind string) bool {
	switch kind {
	case documentKindHTML, documentKindImage, documentKindMarkdown, documentKindText, documentKindCode:
		return true
	default:
		return false
	}
}

func documentKindTextual(kind string) bool {
	switch kind {
	case documentKindHTML, documentKindMarkdown, documentKindText, documentKindCode:
		return true
	default:
		return false
	}
}

// detectDocumentMIME uses file bytes for authoritative document routing. The
// extension remains an input to classifyDocumentKind for structured formats
// such as OpenXML and source files that intentionally share text/plain.
func detectDocumentMIME(path string) string {
	sample, lookahead, complete, err := readAgentDocumentSample(path, 512)
	if err != nil {
		return "application/octet-stream"
	}
	return detectDocumentSampleMIME(path, sample, lookahead, complete)
}

func validDocumentCommitResult(name string, kind string, declaredMIME string, data []byte) bool {
	declaredMIME = normalizeDocumentMIME(declaredMIME)
	if kind == documentKindImage {
		detected := normalizeDocumentMIME(http.DetectContentType(data))
		if detected != "image/png" && detected != "image/jpeg" && detected != "image/webp" {
			return false
		}
		return detected == declaredMIME && classifyDocumentKind(name, detected, data, nil, true) == documentKindImage
	}
	if !documentSampleIsText(data, nil, true) {
		return false
	}
	return classifyDocumentKind(name, declaredMIME, data, nil, true) == kind
}
