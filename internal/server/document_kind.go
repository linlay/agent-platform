package server

import (
	"net/http"

	"agent-platform/internal/documentmeta"
)

const (
	headerDocumentKind     = "X-Document-Kind"
	headerDocumentRevision = "X-Document-Revision"
)

func documentKindEditable(kind string) bool {
	return kind == documentmeta.KindImage || documentKindTextual(kind)
}

func documentKindTextual(kind string) bool {
	switch kind {
	case documentmeta.KindHTML, documentmeta.KindMarkdown, documentmeta.KindText, documentmeta.KindCode:
		return true
	default:
		return false
	}
}

func validDocumentCommitResult(name string, kind string, declaredMIME string, data []byte) bool {
	declaredMIME = documentmeta.NormalizeMIME(declaredMIME)
	if kind == documentmeta.KindImage {
		detected := documentmeta.NormalizeMIME(http.DetectContentType(data))
		if detected != "image/png" && detected != "image/jpeg" && detected != "image/webp" {
			return false
		}
		return detected == declaredMIME && documentmeta.Classify(name, detected, data, nil, true) == documentmeta.KindImage
	}
	if !documentmeta.SampleIsText(data, nil, true) {
		return false
	}
	return documentmeta.Classify(name, declaredMIME, data, nil, true) == kind
}
