package kbase

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/supportpkg"
)

func TestExtractDOCXNativeText(t *testing.T) {
	docx := zipFixture(t, map[string]string{
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Overview</w:t></w:r></w:p>
    <w:p><w:r><w:t>Alpha paragraph</w:t></w:r></w:p>
    <w:tbl><w:tr><w:tc><w:p><w:r><w:t>Table cell text</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
  </w:body>
</w:document>`,
	})
	doc, err := extractDOCX(docx, ExtractionConfig{
		DOCX: DOCXExtractionConfig{Enabled: true, Backend: "native"},
	})
	if err != nil {
		t.Fatalf("extract docx: %v", err)
	}
	text := extractedText(doc)
	for _, want := range []string{"# Overview", "Alpha paragraph", "Table cell text"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected docx text to contain %q, got:\n%s", want, text)
		}
	}
	if doc.Extractor != "docx:native" || len(doc.Blocks) != 1 || doc.Blocks[0].SourceType != "docx" {
		t.Fatalf("unexpected docx metadata: %#v", doc)
	}
}

func TestExtractPPTXNativeSlidesAndNotes(t *testing.T) {
	pptx := zipFixture(t, map[string]string{
		"ppt/presentation.xml": `<?xml version="1.0" encoding="UTF-8"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>
    <p:sldId id="256" r:id="rId2"/>
    <p:sldId id="257" r:id="rId3"/>
  </p:sldIdLst>
</p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide2.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
</Relationships>`,
		"ppt/slides/slide2.xml": `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>First ordered slide</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld>
</p:sld>`,
		"ppt/slides/slide1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Second ordered slide</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld>
</p:sld>`,
		"ppt/slides/_rels/slide2.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rNotes" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide2.xml"/>
</Relationships>`,
		"ppt/notesSlides/notesSlide2.xml": `<?xml version="1.0" encoding="UTF-8"?>
<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Speaker note text</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld>
</p:notes>`,
	})
	doc, err := extractPPTX(pptx, ExtractionConfig{
		PPTX: PPTXExtractionConfig{Enabled: true, Backend: "native", IncludeNotes: true},
	})
	if err != nil {
		t.Fatalf("extract pptx: %v", err)
	}
	if len(doc.Blocks) != 2 {
		t.Fatalf("expected two slide blocks, got %#v", doc.Blocks)
	}
	if doc.Blocks[0].SlideStart != 1 || !strings.Contains(doc.Blocks[0].Content, "First ordered slide") {
		t.Fatalf("unexpected first slide block: %#v", doc.Blocks[0])
	}
	if !strings.Contains(doc.Blocks[0].Content, "Speaker note text") {
		t.Fatalf("expected notes text in first slide block, got %q", doc.Blocks[0].Content)
	}
	if doc.Blocks[1].SlideStart != 2 || !strings.Contains(doc.Blocks[1].Content, "Second ordered slide") {
		t.Fatalf("unexpected second slide block: %#v", doc.Blocks[1])
	}
}

func TestExtractHTMLNativeTextDropsScriptStyleAndHiddenNodes(t *testing.T) {
	doc, err := extractHTML([]byte(`<!doctype html>
<html>
<head>
  <title>Head title</title>
  <style>.noise { color: red; }</style>
  <script>console.log("secret script")</script>
</head>
<body>
  <h2>Guide Title</h2>
  <p>Visible alpha content.</p>
  <ul><li>Visible beta item</li><li hidden>Hidden beta item</li></ul>
  <p aria-hidden="true">Hidden aria content</p>
  <p style="display:none">Hidden display content</p>
  <p style="visibility: hidden">Hidden visibility content</p>
</body>
</html>`))
	if err != nil {
		t.Fatalf("extract html: %v", err)
	}
	if doc.Extractor != "html:native" || doc.Mime != "text/html" || len(doc.Blocks) != 1 || doc.Blocks[0].SourceType != "html" {
		t.Fatalf("unexpected html metadata: %#v", doc)
	}
	text := extractedText(doc)
	for _, want := range []string{"## Guide Title", "Visible alpha content.", "- Visible beta item"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected html text to contain %q, got:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"Head title", "noise", "secret script", "Hidden beta item", "Hidden aria content", "Hidden display content", "Hidden visibility content"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("expected html text to omit %q, got:\n%s", forbidden, text)
		}
	}
}

func TestExtractPDFMissingPopplerSkips(t *testing.T) {
	_, err := extractPDF(context.Background(), "missing.pdf", ExtractionConfig{
		Timeout: 1,
		PDF: PDFExtractionConfig{
			Enabled: true,
			Backend: "poppler",
			Binary:  "definitely-missing-pdftotext-for-kbase-test",
		},
	}, nil)
	var exErr extractionError
	if !errors.As(err, &exErr) || !exErr.skipped || exErr.reason != "pdf_extractor_unavailable" {
		t.Fatalf("expected pdf extractor skip, got %#v %v", exErr, err)
	}
}

func TestResolvePDFBinaryUsesSupportPackageForDefaultNames(t *testing.T) {
	support, binaryPath := supportRegistryFixture(t)
	for _, configured := range []string{"", "pdftotext", "pdftotext.exe", "PDFTOTEXT.EXE"} {
		if got := resolvePDFBinary(configured, support); got != binaryPath {
			t.Fatalf("resolvePDFBinary(%q) = %q, want %q", configured, got, binaryPath)
		}
	}
}

func TestResolvePDFBinaryKeepsExplicitBinary(t *testing.T) {
	support, _ := supportRegistryFixture(t)
	explicit := filepath.Join(t.TempDir(), "custom-pdftotext.exe")
	if got := resolvePDFBinary(explicit, support); got != explicit {
		t.Fatalf("expected explicit binary to win, got %q want %q", got, explicit)
	}
	if got := resolvePDFBinary(`C:\Tools\pdftotext.exe`, support); got != `C:\Tools\pdftotext.exe` {
		t.Fatalf("expected windows absolute binary to win, got %q", got)
	}
}

func TestResolvePDFBinaryFallsBackWithoutSupportPackage(t *testing.T) {
	if got := resolvePDFBinary("pdftotext.exe", nil); got != "pdftotext.exe" {
		t.Fatalf("expected original binary without support package, got %q", got)
	}
}

func supportRegistryFixture(t *testing.T) (*supportpkg.Registry, string) {
	t.Helper()
	root := t.TempDir()
	pluginDir := filepath.Join(root, "pdf-extractor")
	binaryPath := filepath.Join(pluginDir, "payload", "windows-amd64", "Library", "bin", "pdftotext.exe")
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		t.Fatalf("mkdir binary dir: %v", err)
	}
	if err := os.WriteFile(binaryPath, []byte("fake"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	manifest := `{
  "kind": "support-package",
  "id": "pdf-extractor",
  "version": "v0.3.9",
  "platform": { "os": "testos", "arch": "testarch" },
  "executables": {
    "pdftotext": "payload/windows-amd64/Library/bin/pdftotext.exe"
  }
}`
	if err := os.WriteFile(filepath.Join(pluginDir, supportpkg.ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	registry, errs := supportpkg.LoadDir(root, supportpkg.Target{OS: "testos", Arch: "testarch"})
	if len(errs) != 0 {
		t.Fatalf("load support registry: %v", errs)
	}
	return registry, binaryPath
}

func zipFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	for _, name := range names {
		file, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := file.Write([]byte(files[name])); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}
