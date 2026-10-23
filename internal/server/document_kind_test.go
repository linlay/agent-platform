package server

import (
	"agent-platform/internal/documentmeta"
	"os"
	"path/filepath"
	"testing"
)

func TestClassifyDocumentKindUsesSignatureThenSpecializedExtension(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		mimeType string
		sample   []byte
		want     string
	}{
		{name: "html", fileName: "index.html", mimeType: "text/html", sample: []byte("<!doctype html>"), want: documentmeta.KindHTML},
		{name: "office zip before archive", fileName: "report.xlsx", mimeType: "application/zip", sample: []byte("PK\x03\x04\x00"), want: documentmeta.KindOffice},
		{name: "svg before text", fileName: "diagram.svg", mimeType: "text/xml", sample: []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>"), want: documentmeta.KindImage},
		{name: "markdown extension refines text", fileName: "README.md", mimeType: "text/plain", sample: []byte("# hello"), want: documentmeta.KindMarkdown},
		{name: "source extension refines text", fileName: "main.go", mimeType: "text/plain", sample: []byte("package main"), want: documentmeta.KindCode},
		{name: "extensionless utf8", fileName: "NOTICE", mimeType: "text/plain", sample: []byte("hello"), want: documentmeta.KindText},
		{name: "forged image extension remains text", fileName: "notes.png", mimeType: "text/plain", sample: []byte("not an image"), want: documentmeta.KindText},
		{name: "unknown binary", fileName: "payload", mimeType: "application/octet-stream", sample: []byte{0, 1, 2, 3}, want: documentmeta.KindBinary},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := documentmeta.Classify(test.fileName, test.mimeType, test.sample, nil, true); got != test.want {
				t.Fatalf("documentmeta.Classify() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDetectAgentFileMIMEUsesBytesAndRecognizesSafeSVG(t *testing.T) {
	dir := t.TempDir()
	forged := filepath.Join(dir, "forged.png")
	if err := os.WriteFile(forged, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if meta, err := documentmeta.ResolveFile(forged, forged); err != nil || documentmeta.NormalizeMIME(meta.MIMEType) != "text/plain" {
		t.Fatalf("forged image metadata = %+v, err=%v", meta, err)
	}
	svg := filepath.Join(dir, "safe.svg")
	if err := os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if meta, err := documentmeta.ResolveFile(svg, svg); err != nil || documentmeta.NormalizeMIME(meta.MIMEType) != "image/svg+xml" {
		t.Fatalf("SVG metadata = %+v, err=%v", meta, err)
	}
}

func TestValidDocumentCommitResultRejectsMismatchedPayload(t *testing.T) {
	if validDocumentCommitResult("image.png", documentmeta.KindImage, "image/png", []byte("not png")) {
		t.Fatal("forged image payload must be rejected")
	}
	if !validDocumentCommitResult("README.md", documentmeta.KindMarkdown, "text/markdown", []byte("# edited")) {
		t.Fatal("valid UTF-8 markdown payload should be accepted")
	}
	if validDocumentCommitResult("README.md", documentmeta.KindMarkdown, "text/markdown", []byte{0xff}) {
		t.Fatal("invalid UTF-8 text payload must be rejected")
	}
}
