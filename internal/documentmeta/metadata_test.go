package documentmeta

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSafeUTF8TextMetadata(t *testing.T) {
	tests := []struct {
		name         string
		fileName     string
		detectedMIME string
		sample       []byte
		wantKind     string
		wantMIME     string
	}{
		{name: "Chinese markdown", fileName: "小说.md", detectedMIME: "application/octet-stream", sample: []byte("# 第一章\n正文"), wantKind: KindMarkdown, wantMIME: "text/markdown; charset=utf-8"},
		{name: "markdown long extension", fileName: "README.markdown", detectedMIME: "application/octet-stream", sample: []byte("# readme"), wantKind: KindMarkdown, wantMIME: "text/markdown; charset=utf-8"},
		{name: "MDX", fileName: "page.mdx", detectedMIME: "application/octet-stream", sample: []byte("# title"), wantKind: KindMarkdown, wantMIME: "text/markdown; charset=utf-8"},
		{name: "UTF-8 BOM", fileName: "README.md", detectedMIME: "application/octet-stream", sample: append([]byte{0xef, 0xbb, 0xbf}, []byte("# title")...), wantKind: KindMarkdown, wantMIME: "text/markdown; charset=utf-8"},
		{name: "empty markdown", fileName: "empty.md", detectedMIME: "application/octet-stream", sample: nil, wantKind: KindMarkdown, wantMIME: "text/markdown; charset=utf-8"},
		{name: "text", fileName: "notes.txt", detectedMIME: "application/octet-stream", sample: []byte("plain text"), wantKind: KindText, wantMIME: "text/plain; charset=utf-8"},
		{name: "extensionless text", fileName: "NOTICE", detectedMIME: "application/octet-stream", sample: []byte("plain text"), wantKind: KindText, wantMIME: "text/plain; charset=utf-8"},
		{name: "invalid UTF-8 markdown", fileName: "bad.md", detectedMIME: "application/octet-stream", sample: []byte{0xff, 0xfe, 0x01}, wantKind: KindBinary, wantMIME: "application/octet-stream"},
		{name: "invalid UTF-8 despite declared markdown MIME", fileName: "declared.md", detectedMIME: "text/markdown", sample: []byte{0xff, 0xfe, 0x01}, wantKind: KindBinary, wantMIME: "application/octet-stream"},
		{name: "NUL text", fileName: "bad.txt", detectedMIME: "application/octet-stream", sample: []byte{'a', 0, 'b'}, wantKind: KindBinary, wantMIME: "application/octet-stream"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Resolve(test.fileName, test.detectedMIME, test.sample, nil, true)
			if got.DocumentKind != test.wantKind || got.MIMEType != test.wantMIME {
				t.Fatalf("Resolve() = %#v, want kind=%q MIME=%q", got, test.wantKind, test.wantMIME)
			}
		})
	}
}

func TestResolveKeepsSpecificOpenXMLMIME(t *testing.T) {
	got := Resolve("report.docx", "application/zip", []byte("PK\x03\x04\x00"), nil, true)
	if got.DocumentKind != KindOffice || got.MIMEType != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Fatalf("Resolve() = %#v", got)
	}
}

func TestSampleIsTextAcceptsOnlyIncompleteTrailingUTF8Rune(t *testing.T) {
	tests := []struct {
		name      string
		sample    []byte
		lookahead []byte
	}{
		{
			name:      "three-byte rune with one byte outside",
			sample:    append(bytes.Repeat([]byte("a"), 510), []byte{0xe5, 0xb7}...),
			lookahead: []byte{0xa5},
		},
		{
			name:      "three-byte rune with two bytes outside",
			sample:    append(bytes.Repeat([]byte("a"), 511), 0xe5),
			lookahead: []byte{0xb7, 0xa5},
		},
		{
			name:      "four-byte rune with one byte outside",
			sample:    append(bytes.Repeat([]byte("a"), 509), []byte{0xf0, 0x9f, 0x98}...),
			lookahead: []byte{0x80},
		},
		{
			name:      "four-byte rune with two bytes outside",
			sample:    append(bytes.Repeat([]byte("a"), 510), []byte{0xf0, 0x9f}...),
			lookahead: []byte{0x98, 0x80},
		},
		{
			name:      "four-byte rune with three bytes outside",
			sample:    append(bytes.Repeat([]byte("a"), 511), 0xf0),
			lookahead: []byte{0x9f, 0x98, 0x80},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !SampleIsText(test.sample, test.lookahead, false) {
				t.Fatal("fixed-size UTF-8 prefix ending inside a rune was classified as binary")
			}
		})
	}

	truncatedPrefix := tests[0].sample
	if SampleIsText(truncatedPrefix, nil, true) {
		t.Fatal("complete content validation accepted an incomplete trailing rune")
	}
	if SampleIsText(truncatedPrefix, []byte{'x'}, false) {
		t.Fatal("invalid UTF-8 continuation after the sample boundary was classified as text")
	}
	fourBytePrefix := tests[4].sample
	if !SampleIsText(fourBytePrefix, []byte{0x9f, 0x98, 0x80}, false) {
		t.Fatal("four-byte UTF-8 rune crossing the sample boundary was classified as binary")
	}
	if SampleIsText(fourBytePrefix, []byte{0x9f, 'x', 0x80}, false) {
		t.Fatal("malformed four-byte UTF-8 rune crossing the sample boundary was classified as text")
	}
	if SampleIsText([]byte{'a', 0xff, 'b'}, nil, false) {
		t.Fatal("malformed UTF-8 inside the sample was classified as text")
	}
	if SampleIsText([]byte{'a', 0, 'b'}, nil, false) {
		t.Fatal("NUL-containing sample was classified as text")
	}
}

func TestResolveFileDistinguishesPrefixBoundaryFromIncompleteContent(t *testing.T) {
	validPath := filepath.Join(t.TempDir(), "空位.md")
	validBody := append(bytes.Repeat([]byte("a"), 510), []byte("工作正文")...)
	if err := os.WriteFile(validPath, validBody, 0o644); err != nil {
		t.Fatal(err)
	}
	valid, err := ResolveFile(validPath, filepath.Base(validPath))
	if err != nil {
		t.Fatal(err)
	}
	if valid.DocumentKind != KindMarkdown || valid.MIMEType != "text/markdown; charset=utf-8" {
		t.Fatalf("valid boundary metadata=%#v", valid)
	}

	invalidPath := filepath.Join(t.TempDir(), "残缺.md")
	invalidBody := append(bytes.Repeat([]byte("a"), 510), []byte{0xe5, 0xb7}...)
	if err := os.WriteFile(invalidPath, invalidBody, 0o644); err != nil {
		t.Fatal(err)
	}
	invalid, err := ResolveFile(invalidPath, filepath.Base(invalidPath))
	if err != nil {
		t.Fatal(err)
	}
	if invalid.DocumentKind != KindBinary || invalid.MIMEType != "application/octet-stream" {
		t.Fatalf("incomplete content metadata=%#v", invalid)
	}
}
