package kbase

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/config"
	"agent-platform/internal/supportpkg"
	"agent-platform/internal/textcodec"
)

type extractedDocument struct {
	Blocks    []extractedBlock
	Metadata  map[string]any
	Extractor string
	Mime      string
}

type extractedBlock struct {
	SourceType string
	Heading    string
	Content    string
	StartLine  int
	EndLine    int
	PageStart  int
	PageEnd    int
	SlideStart int
	SlideEnd   int
}

type extractionError struct {
	reason  string
	message string
	skipped bool
}

func (e extractionError) Error() string {
	if strings.TrimSpace(e.message) != "" {
		return e.message
	}
	return e.reason
}

func extractionSkip(reason string) error {
	return extractionError{reason: reason, skipped: true}
}

func extractionFailure(reason string, err error) error {
	if err == nil {
		return extractionError{reason: reason, message: reason}
	}
	return extractionError{reason: reason, message: err.Error()}
}

func effectiveExtractionConfig(cfg config.KBaseExtractionConfig) config.KBaseExtractionConfig {
	if cfg.Timeout <= 0 &&
		cfg.MaxFileBytes <= 0 &&
		cfg.PDF.Backend == "" && cfg.PDF.Binary == "" && !cfg.PDF.Enabled &&
		cfg.DOCX.Backend == "" && !cfg.DOCX.Enabled &&
		cfg.PPTX.Backend == "" && !cfg.PPTX.Enabled && !cfg.PPTX.IncludeNotes {
		cfg = config.KBaseExtractionConfig{
			Timeout:      60 * time.Second,
			MaxFileBytes: defaultMaxFileBytes,
			PDF:          config.KBasePDFExtractionConfig{Enabled: true, Backend: "poppler", Binary: "pdftotext"},
			DOCX:         config.KBaseDOCXExtractionConfig{Enabled: true, Backend: "native"},
			PPTX:         config.KBasePPTXExtractionConfig{Enabled: true, Backend: "native", IncludeNotes: true},
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = defaultMaxFileBytes
	}
	if cfg.PDF.Backend == "" {
		cfg.PDF.Backend = "poppler"
	}
	if cfg.PDF.Binary == "" {
		cfg.PDF.Binary = "pdftotext"
	}
	if cfg.DOCX.Backend == "" {
		cfg.DOCX.Backend = "native"
	}
	if cfg.PPTX.Backend == "" {
		cfg.PPTX.Backend = "native"
	}
	cfg.PDF.Backend = strings.ToLower(strings.TrimSpace(cfg.PDF.Backend))
	cfg.DOCX.Backend = strings.ToLower(strings.TrimSpace(cfg.DOCX.Backend))
	cfg.PPTX.Backend = strings.ToLower(strings.TrimSpace(cfg.PPTX.Backend))
	return cfg
}

func extractionMaxFileBytes(cfg config.KBaseExtractionConfig) int64 {
	cfg = effectiveExtractionConfig(cfg)
	return cfg.MaxFileBytes
}

func extractDocument(ctx context.Context, fullPath string, rel string, ext string, data []byte, cfg config.KBaseExtractionConfig, support *supportpkg.Registry) (extractedDocument, error) {
	cfg = effectiveExtractionConfig(cfg)
	switch ext {
	case ".pdf":
		return extractPDF(ctx, fullPath, cfg, support)
	case ".docx":
		return extractDOCX(data, cfg)
	case ".pptx":
		return extractPPTX(data, cfg)
	case ".html", ".htm":
		return extractHTML(data)
	default:
		if _, ok := supportedTextExtensions[ext]; !ok {
			return extractedDocument{}, extractionSkip("unsupported_extension")
		}
		return extractPlainText(rel, ext, data)
	}
}

func extractorNameForExtension(ext string, cfg config.KBaseExtractionConfig) string {
	cfg = effectiveExtractionConfig(cfg)
	switch ext {
	case ".pdf":
		return "pdf:" + cfg.PDF.Backend
	case ".docx":
		return "docx:" + cfg.DOCX.Backend
	case ".pptx":
		return "pptx:" + cfg.PPTX.Backend
	case ".html", ".htm":
		return "html:native"
	default:
		if _, ok := supportedTextExtensions[ext]; ok {
			return "text:native"
		}
		return ""
	}
}

func mimeForExtension(ext string) string {
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".md", ".markdown":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".csv":
		return "text/csv"
	case ".html", ".htm":
		return "text/html"
	default:
		if _, ok := supportedTextExtensions[ext]; ok {
			return "text/plain"
		}
		return ""
	}
}

func extractPlainText(rel string, ext string, data []byte) (extractedDocument, error) {
	text := string(data)
	lineCount := countLines(text)
	return extractedDocument{
		Extractor: "text:native",
		Mime:      mimeForExtension(ext),
		Metadata:  map[string]any{"lineCount": lineCount},
		Blocks: []extractedBlock{{
			SourceType: "text",
			Content:    text,
			StartLine:  1,
			EndLine:    lineCount,
		}},
	}, nil
}

func extractHTML(data []byte) (extractedDocument, error) {
	if looksBinary(data) {
		return extractedDocument{}, extractionSkip("binary_or_non_utf8")
	}
	text := textcodec.HTMLToMarkdownLike(data)
	if strings.TrimSpace(text) == "" {
		return extractedDocument{}, extractionSkip("html_no_text")
	}
	lineCount := countLines(text)
	return extractedDocument{
		Extractor: "html:native",
		Mime:      mimeForExtension(".html"),
		Metadata:  map[string]any{"lineCount": lineCount},
		Blocks: []extractedBlock{{
			SourceType: "html",
			Content:    text,
			StartLine:  1,
			EndLine:    lineCount,
		}},
	}, nil
}

func extractPDF(ctx context.Context, fullPath string, cfg config.KBaseExtractionConfig, support *supportpkg.Registry) (extractedDocument, error) {
	if !cfg.PDF.Enabled {
		return extractedDocument{}, extractionSkip("pdf_extractor_disabled")
	}
	if cfg.PDF.Backend != "poppler" {
		return extractedDocument{}, extractionSkip("pdf_extractor_unavailable")
	}
	binary := strings.TrimSpace(cfg.PDF.Binary)
	if binary == "" {
		binary = "pdftotext"
	}
	binary = resolvePDFBinary(binary, support)
	if _, err := exec.LookPath(binary); err != nil {
		return extractedDocument{}, extractionSkip("pdf_extractor_unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	cmd := exec.CommandContext(callCtx, binary, "-layout", "-enc", "UTF-8", fullPath, "-")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return extractedDocument{}, extractionFailure("pdf_extractor_timeout", fmt.Errorf("pdftotext timed out after %s", cfg.Timeout))
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return extractedDocument{}, extractionFailure("pdf_extractor_failed", fmt.Errorf("pdftotext failed: %s", truncateForError(message)))
	}
	pages := strings.Split(stdout.String(), "\f")
	blocks := make([]extractedBlock, 0, len(pages))
	lineCursor := 1
	for i, page := range pages {
		pageText := strings.TrimSpace(page)
		if pageText == "" {
			continue
		}
		lineCount := countLines(pageText)
		blocks = append(blocks, extractedBlock{
			SourceType: "pdf",
			Content:    pageText,
			StartLine:  lineCursor,
			EndLine:    lineCursor + lineCount - 1,
			PageStart:  i + 1,
			PageEnd:    i + 1,
		})
		lineCursor += lineCount
	}
	if len(blocks) == 0 {
		return extractedDocument{}, extractionSkip("pdf_no_text_layer")
	}
	return extractedDocument{
		Extractor: "pdf:poppler",
		Mime:      mimeForExtension(".pdf"),
		Metadata:  map[string]any{"pageCount": len(pages)},
		Blocks:    blocks,
	}, nil
}

func resolvePDFBinary(binary string, support *supportpkg.Registry) string {
	binary = strings.TrimSpace(binary)
	if !shouldUseSupportPDFBinary(binary) {
		return binary
	}
	if executable, ok := support.Executable("pdftotext"); ok && strings.TrimSpace(executable.Path) != "" {
		return executable.Path
	}
	return binary
}

func shouldUseSupportPDFBinary(binary string) bool {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		return true
	}
	if strings.ContainsAny(binary, `:\/`) {
		return false
	}
	lower := strings.ToLower(binary)
	return lower == "pdftotext" || lower == "pdftotext.exe"
}

func extractDOCX(data []byte, cfg config.KBaseExtractionConfig) (extractedDocument, error) {
	if !cfg.DOCX.Enabled {
		return extractedDocument{}, extractionSkip("docx_extractor_disabled")
	}
	if cfg.DOCX.Backend != "native" {
		return extractedDocument{}, extractionSkip("docx_extractor_unavailable")
	}
	files, err := openZipBytes(data)
	if err != nil {
		return extractedDocument{}, extractionFailure("docx_invalid_zip", err)
	}
	documentXML, ok, err := readZipFile(files, "word/document.xml")
	if err != nil {
		return extractedDocument{}, extractionFailure("docx_read_failed", err)
	}
	if !ok {
		return extractedDocument{}, extractionSkip("docx_document_xml_missing")
	}
	lines, err := docxLines(documentXML)
	if err != nil {
		return extractedDocument{}, extractionFailure("docx_parse_failed", err)
	}
	content := strings.TrimSpace(strings.Join(lines, "\n"))
	if content == "" {
		return extractedDocument{}, extractionSkip("docx_no_text")
	}
	lineCount := countLines(content)
	return extractedDocument{
		Extractor: "docx:native",
		Mime:      mimeForExtension(".docx"),
		Metadata:  map[string]any{"lineCount": lineCount},
		Blocks: []extractedBlock{{
			SourceType: "docx",
			Content:    content,
			StartLine:  1,
			EndLine:    lineCount,
		}},
	}, nil
}

func extractPPTX(data []byte, cfg config.KBaseExtractionConfig) (extractedDocument, error) {
	if !cfg.PPTX.Enabled {
		return extractedDocument{}, extractionSkip("pptx_extractor_disabled")
	}
	if cfg.PPTX.Backend != "native" {
		return extractedDocument{}, extractionSkip("pptx_extractor_unavailable")
	}
	files, err := openZipBytes(data)
	if err != nil {
		return extractedDocument{}, extractionFailure("pptx_invalid_zip", err)
	}
	slidePaths, err := pptxSlidePaths(files)
	if err != nil {
		return extractedDocument{}, extractionFailure("pptx_parse_failed", err)
	}
	blocks := make([]extractedBlock, 0, len(slidePaths))
	lineCursor := 1
	for i, slidePath := range slidePaths {
		slideXML, ok, err := readZipFile(files, slidePath)
		if err != nil {
			return extractedDocument{}, extractionFailure("pptx_read_failed", err)
		}
		if !ok {
			continue
		}
		lines, err := drawingTextLines(slideXML)
		if err != nil {
			return extractedDocument{}, extractionFailure("pptx_parse_failed", err)
		}
		if cfg.PPTX.IncludeNotes {
			if notesPath := pptxNotesPath(files, slidePath); notesPath != "" {
				if notesXML, ok, err := readZipFile(files, notesPath); err != nil {
					return extractedDocument{}, extractionFailure("pptx_read_failed", err)
				} else if ok {
					notes, err := drawingTextLines(notesXML)
					if err != nil {
						return extractedDocument{}, extractionFailure("pptx_parse_failed", err)
					}
					if len(notes) > 0 {
						lines = append(lines, "Notes:")
						lines = append(lines, notes...)
					}
				}
			}
		}
		content := strings.TrimSpace(strings.Join(lines, "\n"))
		if content == "" {
			continue
		}
		lineCount := countLines(content)
		blocks = append(blocks, extractedBlock{
			SourceType: "pptx",
			Heading:    firstNonEmptyLine(lines),
			Content:    content,
			StartLine:  lineCursor,
			EndLine:    lineCursor + lineCount - 1,
			SlideStart: i + 1,
			SlideEnd:   i + 1,
		})
		lineCursor += lineCount
	}
	if len(blocks) == 0 {
		return extractedDocument{}, extractionSkip("pptx_no_text")
	}
	return extractedDocument{
		Extractor: "pptx:native",
		Mime:      mimeForExtension(".pptx"),
		Metadata:  map[string]any{"slideCount": len(slidePaths), "includeNotes": cfg.PPTX.IncludeNotes},
		Blocks:    blocks,
	}, nil
}

func openZipBytes(data []byte) ([]*zip.File, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	return reader.File, nil
}

func readZipFile(files []*zip.File, name string) ([]byte, bool, error) {
	name = path.Clean(strings.TrimPrefix(name, "/"))
	for _, file := range files {
		if path.Clean(file.Name) != name {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, true, err
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		return data, true, err
	}
	return nil, false, nil
}

func docxLines(data []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var lines []string
	var current strings.Builder
	inParagraph := false
	inText := false
	style := ""
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch tok := token.(type) {
		case xml.StartElement:
			if tok.Name.Local == "p" && !inParagraph {
				inParagraph = true
				inText = false
				style = ""
				current.Reset()
				continue
			}
			if !inParagraph {
				continue
			}
			switch tok.Name.Local {
			case "pStyle":
				style = attrLocal(tok.Attr, "val")
			case "t":
				inText = true
			case "tab":
				current.WriteByte('\t')
			case "br", "cr":
				current.WriteByte('\n')
			}
		case xml.EndElement:
			if !inParagraph {
				continue
			}
			if tok.Name.Local == "t" {
				inText = false
				continue
			}
			if tok.Name.Local == "p" {
				line := strings.TrimSpace(current.String())
				if line != "" {
					line = withDocxHeadingPrefix(style, line)
					lines = append(lines, line)
				}
				inParagraph = false
				inText = false
			}
		case xml.CharData:
			if inParagraph && inText {
				current.Write([]byte(tok))
			}
		}
	}
	return lines, nil
}

func withDocxHeadingPrefix(style string, line string) string {
	level := docxHeadingLevel(style)
	if level <= 0 {
		return line
	}
	if level > 6 {
		level = 6
	}
	return strings.Repeat("#", level) + " " + strings.TrimSpace(line)
}

func docxHeadingLevel(style string) int {
	style = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(style), " ", ""))
	if style == "title" {
		return 1
	}
	if !strings.HasPrefix(style, "heading") {
		return 0
	}
	raw := strings.TrimPrefix(style, "heading")
	if raw == "" {
		return 1
	}
	level, err := strconv.Atoi(raw)
	if err != nil {
		return 1
	}
	return level
}

func pptxSlidePaths(files []*zip.File) ([]string, error) {
	presentationXML, ok, err := readZipFile(files, "ppt/presentation.xml")
	if err != nil {
		return nil, err
	}
	relsXML, relsOK, err := readZipFile(files, "ppt/_rels/presentation.xml.rels")
	if err != nil {
		return nil, err
	}
	if ok && relsOK {
		ids, err := presentationSlideIDs(presentationXML)
		if err != nil {
			return nil, err
		}
		rels, err := relationshipTargets(relsXML)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, id := range ids {
			target := rels[id]
			if target == "" {
				continue
			}
			out = append(out, normalizeZipTarget("ppt", target))
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	var out []string
	for _, file := range files {
		name := path.Clean(file.Name)
		if strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") && !strings.Contains(name, "/_rels/") {
			out = append(out, name)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return slidePathOrdinal(out[i]) < slidePathOrdinal(out[j])
	})
	return out, nil
}

func presentationSlideIDs(data []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var ids []string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return ids, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "sldId" {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local == "id" && strings.HasPrefix(attr.Value, "rId") {
				ids = append(ids, attr.Value)
				break
			}
		}
	}
}

func relationshipTargets(data []byte) (map[string]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	out := map[string]string{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		id := attrLocal(start.Attr, "Id")
		target := attrLocal(start.Attr, "Target")
		if id != "" && target != "" {
			out[id] = target
		}
	}
}

func pptxNotesPath(files []*zip.File, slidePath string) string {
	relsPath := path.Join(path.Dir(slidePath), "_rels", path.Base(slidePath)+".rels")
	relsXML, ok, err := readZipFile(files, relsPath)
	if err == nil && ok {
		if rels, relsErr := relationshipTargetsWithTypes(relsXML); relsErr == nil {
			for _, rel := range rels {
				if strings.Contains(rel.Type, "/notesSlide") || strings.Contains(rel.Target, "notesSlides") {
					return normalizeZipTarget(path.Dir(slidePath), rel.Target)
				}
			}
		}
	}
	fallback := "ppt/notesSlides/notesSlide" + strconv.Itoa(slidePathOrdinal(slidePath)) + ".xml"
	if _, ok, _ := readZipFile(files, fallback); ok {
		return fallback
	}
	return ""
}

type relationshipTarget struct {
	Target string
	Type   string
}

func relationshipTargetsWithTypes(data []byte) ([]relationshipTarget, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var out []relationshipTarget
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		target := attrLocal(start.Attr, "Target")
		if target == "" {
			continue
		}
		out = append(out, relationshipTarget{Target: target, Type: attrLocal(start.Attr, "Type")})
	}
}

func drawingTextLines(data []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var lines []string
	var current strings.Builder
	inParagraph := false
	inText := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch tok := token.(type) {
		case xml.StartElement:
			if isDrawingParagraph(tok.Name) && !inParagraph {
				inParagraph = true
				current.Reset()
				continue
			}
			if inParagraph && tok.Name.Local == "t" {
				inText = true
			}
		case xml.EndElement:
			if inParagraph && tok.Name.Local == "t" {
				inText = false
				continue
			}
			if inParagraph && isDrawingParagraph(tok.Name) {
				line := strings.TrimSpace(current.String())
				if line != "" {
					lines = append(lines, line)
				}
				inParagraph = false
				inText = false
			}
		case xml.CharData:
			if inParagraph && inText {
				current.Write([]byte(tok))
			}
		}
	}
	return lines, nil
}

func isDrawingParagraph(name xml.Name) bool {
	return name.Local == "p" && strings.Contains(name.Space, "/drawingml/")
}

func normalizeZipTarget(baseDir string, target string) string {
	target = strings.TrimSpace(strings.TrimPrefix(target, "/"))
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "ppt/") {
		return path.Clean(target)
	}
	return path.Clean(path.Join(baseDir, target))
}

func slidePathOrdinal(slidePath string) int {
	base := path.Base(slidePath)
	base = strings.TrimSuffix(strings.TrimPrefix(base, "slide"), ".xml")
	value, err := strconv.Atoi(base)
	if err != nil {
		return 0
	}
	return value
}

func attrLocal(attrs []xml.Attr, name string) string {
	for _, attr := range attrs {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func firstNonEmptyLine(lines []string) string {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && line != "Notes:" {
			return line
		}
	}
	return ""
}

func countLines(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func truncateForError(text string) string {
	const max = 400
	text = strings.TrimSpace(text)
	if len([]rune(text)) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "..."
}
