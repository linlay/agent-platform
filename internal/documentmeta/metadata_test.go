package documentmeta

import "testing"

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
			got := Resolve(test.fileName, test.detectedMIME, test.sample)
			if got.DocumentKind != test.wantKind || got.MIMEType != test.wantMIME {
				t.Fatalf("Resolve() = %#v, want kind=%q MIME=%q", got, test.wantKind, test.wantMIME)
			}
		})
	}
}

func TestResolveKeepsSpecificOpenXMLMIME(t *testing.T) {
	got := Resolve("report.docx", "application/zip", []byte("PK\x03\x04\x00"))
	if got.DocumentKind != KindOffice || got.MIMEType != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Fatalf("Resolve() = %#v", got)
	}
}
