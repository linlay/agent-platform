package documentmeta

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	KindHTML     = "document-html"
	KindImage    = "document-image"
	KindMarkdown = "document-markdown"
	KindText     = "document-text"
	KindCode     = "document-code"
	KindPDF      = "document-pdf"
	KindOffice   = "document-office"
	KindAudio    = "document-audio"
	KindVideo    = "document-video"
	KindArchive  = "document-archive"
	KindBinary   = "document-binary"
)

type Metadata struct {
	DocumentKind string
	MIMEType     string
}

var imageExtensions = map[string]bool{
	".apng": true, ".avif": true, ".bmp": true, ".gif": true, ".heic": true,
	".heif": true, ".ico": true, ".jpeg": true, ".jpg": true, ".png": true,
	".svg": true, ".tif": true, ".tiff": true, ".webp": true,
}

var codeExtensions = map[string]bool{
	".c": true, ".cc": true, ".cpp": true, ".css": true, ".go": true, ".h": true,
	".hpp": true, ".ini": true, ".java": true, ".js": true, ".json": true,
	".jsx": true, ".mjs": true, ".py": true, ".rb": true, ".rs": true, ".sh": true,
	".sql": true, ".toml": true, ".ts": true, ".tsx": true, ".xml": true,
	".yaml": true, ".yml": true,
}

var officeExtensions = map[string]bool{
	".doc": true, ".docm": true, ".docx": true, ".dot": true, ".dotm": true,
	".dotx": true, ".pot": true, ".potm": true, ".potx": true, ".pps": true,
	".ppsm": true, ".ppsx": true, ".ppt": true, ".pptm": true, ".pptx": true,
	".xla": true, ".xlam": true, ".xls": true, ".xlsb": true, ".xlsm": true,
	".xlsx": true, ".xlt": true, ".xltm": true, ".xltx": true,
}

var audioExtensions = map[string]bool{
	".aac": true, ".flac": true, ".m4a": true, ".mp3": true, ".oga": true,
	".ogg": true, ".opus": true, ".wav": true, ".weba": true,
}

var videoExtensions = map[string]bool{
	".m4v": true, ".mov": true, ".mp4": true, ".mpeg": true, ".mpg": true,
	".ogv": true, ".webm": true,
}

var archiveExtensions = map[string]bool{
	".7z": true, ".bz2": true, ".gz": true, ".rar": true, ".tar": true,
	".tgz": true, ".xz": true, ".zip": true,
}

func NormalizeMIME(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if parsed, _, err := mime.ParseMediaType(value); err == nil {
		return parsed
	}
	return strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
}

func SampleIsText(sample []byte, lookahead []byte, complete bool) bool {
	if len(sample) == 0 {
		return true
	}
	if bytes.ContainsRune(sample, 0) {
		return false
	}
	if utf8.Valid(sample) {
		return true
	}
	if complete {
		return false
	}

	// A fixed-size prefix can end inside a multi-byte rune. Locate a valid
	// trailing rune start and use at most UTFMax-1 lookahead bytes to prove
	// that the crossing rune is valid. Invalid bytes elsewhere remain binary.
	maxSuffix := min(len(sample), utf8.UTFMax-1)
	for suffixBytes := 1; suffixBytes <= maxSuffix; suffixBytes++ {
		start := len(sample) - suffixBytes
		if !utf8.RuneStart(sample[start]) || !utf8.Valid(sample[:start]) {
			continue
		}
		crossing := make([]byte, 0, suffixBytes+len(lookahead))
		crossing = append(crossing, sample[start:]...)
		crossing = append(crossing, lookahead...)
		r, size := utf8.DecodeRune(crossing)
		if r == utf8.RuneError && size == 1 {
			return false
		}
		return size > suffixBytes
	}
	return false
}

func Resolve(name string, detectedMIME string, sample []byte, lookahead []byte, complete bool) Metadata {
	kind := Classify(name, detectedMIME, sample, lookahead, complete)
	return Metadata{
		DocumentKind: kind,
		MIMEType:     resolvedMIME(name, kind, detectedMIME),
	}
}

func ResolveFile(path string, semanticName string) (Metadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer file.Close()
	const sampleBytes = 512
	buffer := make([]byte, sampleBytes+utf8.UTFMax-1)
	n, err := io.ReadFull(file, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return Metadata{}, err
	}
	complete := n <= sampleBytes
	sampleEnd := min(n, sampleBytes)
	sample := buffer[:sampleEnd]
	var lookahead []byte
	if n > sampleEnd {
		lookahead = buffer[sampleEnd:n]
	}
	detectedMIME := DetectMIME(semanticName, sample, lookahead, complete)
	return Resolve(semanticName, detectedMIME, sample, lookahead, complete), nil
}

func DetectMIME(name string, sample []byte, lookahead []byte, complete bool) string {
	if len(sample) == 0 {
		return "application/octet-stream"
	}
	if strings.EqualFold(filepath.Ext(name), ".svg") && SampleIsText(sample, lookahead, complete) &&
		strings.Contains(strings.ToLower(string(sample)), "<svg") {
		return "image/svg+xml"
	}
	return NormalizeMIME(http.DetectContentType(sample))
}

func Classify(name string, mimeType string, sample []byte, lookahead []byte, complete bool) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	mimeType = NormalizeMIME(mimeType)
	isText := SampleIsText(sample, lookahead, complete)

	if strings.HasPrefix(mimeType, "application/vnd.openxmlformats-officedocument.") ||
		mimeType == "application/msword" || strings.HasPrefix(mimeType, "application/vnd.ms-") {
		return KindOffice
	}
	if mimeType == "application/pdf" {
		return KindPDF
	}
	if strings.HasPrefix(mimeType, "image/") {
		return KindImage
	}
	if isText && (mimeType == "text/html" || mimeType == "application/xhtml+xml") {
		return KindHTML
	}
	if isText && mimeType == "text/markdown" {
		return KindMarkdown
	}
	if strings.HasPrefix(mimeType, "audio/") {
		return KindAudio
	}
	if strings.HasPrefix(mimeType, "video/") {
		return KindVideo
	}

	if officeExtensions[ext] && !isText &&
		(mimeType == "application/zip" || mimeType == "application/octet-stream") {
		return KindOffice
	}
	if ext == ".pdf" && !isText {
		return KindPDF
	}
	if imageExtensions[ext] && (!isText || ext == ".svg") {
		return KindImage
	}
	if (ext == ".html" || ext == ".htm" || ext == ".xhtml") && isText {
		return KindHTML
	}
	if (ext == ".md" || ext == ".markdown" || ext == ".mdx") && isText {
		return KindMarkdown
	}
	if audioExtensions[ext] && !isText {
		return KindAudio
	}
	if videoExtensions[ext] && !isText {
		return KindVideo
	}
	if (archiveExtensions[ext] && !isText) || mimeType == "application/zip" ||
		mimeType == "application/x-7z-compressed" || mimeType == "application/x-rar-compressed" {
		return KindArchive
	}
	if isText && (codeExtensions[ext] || strings.Contains(mimeType, "json") || strings.Contains(mimeType, "xml") ||
		strings.Contains(mimeType, "javascript") || strings.Contains(mimeType, "yaml")) {
		return KindCode
	}
	if ((ext == ".txt" || ext == ".log" || ext == ".csv" || ext == ".tsv") && isText) ||
		(strings.HasPrefix(mimeType, "text/") && isText) {
		return KindText
	}
	if isText {
		return KindText
	}
	return KindBinary
}

func resolvedMIME(name string, kind string, detectedMIME string) string {
	base := NormalizeMIME(detectedMIME)
	switch kind {
	case KindMarkdown:
		return mime.FormatMediaType("text/markdown", map[string]string{"charset": "utf-8"})
	case KindText:
		return mime.FormatMediaType("text/plain", map[string]string{"charset": "utf-8"})
	case KindCode:
		if !strings.HasPrefix(base, "text/") &&
			!strings.Contains(base, "json") && !strings.Contains(base, "xml") &&
			!strings.Contains(base, "javascript") && !strings.Contains(base, "yaml") {
			base = "text/plain"
		}
		return mime.FormatMediaType(base, map[string]string{"charset": "utf-8"})
	case KindHTML:
		if base != "application/xhtml+xml" {
			base = "text/html"
		}
		return mime.FormatMediaType(base, map[string]string{"charset": "utf-8"})
	case KindOffice:
		switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
		case ".docx":
			return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		case ".xlsx":
			return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		case ".pptx":
			return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
		}
		if base == "" {
			return "application/octet-stream"
		}
		return base
	case KindBinary:
		if strings.HasPrefix(base, "text/") || base == "" {
			return "application/octet-stream"
		}
		return base
	default:
		if base == "" {
			return "application/octet-stream"
		}
		return base
	}
}
