package server

import (
	"net/http"

	"agent-platform/internal/documentmeta"
)

const (
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

func documentSampleIsText(sample []byte) bool {
	return documentmeta.SampleIsText(sample)
}

func classifyDocumentKind(name string, mimeType string, sample []byte) string {
	return documentmeta.Classify(name, mimeType, sample)
}

func resolveDocumentMetadata(name string, mimeType string, sample []byte) documentmeta.Metadata {
	return documentmeta.Resolve(name, mimeType, sample)
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
	sample, _, err := readAgentFilePrefix(path, 512)
	if err != nil {
		return "application/octet-stream"
	}
	return documentmeta.DetectMIME(path, sample)
}

func validDocumentCommitResult(name string, kind string, declaredMIME string, data []byte) bool {
	declaredMIME = normalizeDocumentMIME(declaredMIME)
	if kind == documentKindImage {
		detected := normalizeDocumentMIME(http.DetectContentType(data))
		if detected != "image/png" && detected != "image/jpeg" && detected != "image/webp" {
			return false
		}
		return detected == declaredMIME && classifyDocumentKind(name, detected, data) == documentKindImage
	}
	if !documentSampleIsText(data) {
		return false
	}
	return classifyDocumentKind(name, declaredMIME, data) == kind
}
