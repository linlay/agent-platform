package server

import (
	"bytes"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	documentKindHTML     = "document-html"
	documentKindImage    = "document-image"
	documentKindMarkdown = "document-markdown"
	documentKindText     = "document-text"
	documentKindCode     = "document-code"
	documentKindPDF      = "document-pdf"
	documentKindOffice   = "document-office"
	documentKindAudio    = "document-audio"
	documentKindVideo    = "document-video"
	documentKindArchive  = "document-archive"
	documentKindBinary   = "document-binary"
)

var documentImageExtensions = map[string]bool{
	".apng": true, ".avif": true, ".bmp": true, ".gif": true, ".heic": true,
	".heif": true, ".ico": true, ".jpeg": true, ".jpg": true, ".png": true,
	".svg": true, ".tif": true, ".tiff": true, ".webp": true,
}

var documentCodeExtensions = map[string]bool{
	".c": true, ".cc": true, ".cpp": true, ".css": true, ".go": true, ".h": true,
	".hpp": true, ".ini": true, ".java": true, ".js": true, ".json": true,
	".jsx": true, ".mjs": true, ".py": true, ".rb": true, ".rs": true, ".sh": true,
	".sql": true, ".toml": true, ".ts": true, ".tsx": true, ".xml": true,
	".yaml": true, ".yml": true,
}

var documentOfficeExtensions = map[string]bool{
	".doc": true, ".docm": true, ".docx": true, ".dot": true, ".dotm": true,
	".dotx": true, ".pot": true, ".potm": true, ".potx": true, ".pps": true,
	".ppsm": true, ".ppsx": true, ".ppt": true, ".pptm": true, ".pptx": true,
	".xla": true, ".xlam": true, ".xls": true, ".xlsb": true, ".xlsm": true,
	".xlsx": true, ".xlt": true, ".xltm": true, ".xltx": true,
}

var documentAudioExtensions = map[string]bool{
	".aac": true, ".flac": true, ".m4a": true, ".mp3": true, ".oga": true,
	".ogg": true, ".opus": true, ".wav": true, ".weba": true,
}

var documentVideoExtensions = map[string]bool{
	".m4v": true, ".mov": true, ".mp4": true, ".mpeg": true, ".mpg": true,
	".ogv": true, ".webm": true,
}

var documentArchiveExtensions = map[string]bool{
	".7z": true, ".bz2": true, ".gz": true, ".rar": true, ".tar": true,
	".tgz": true, ".xz": true, ".zip": true,
}

func normalizeDocumentMIME(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if parsed, _, err := mime.ParseMediaType(value); err == nil {
		return parsed
	}
	return strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
}

func documentSampleIsText(sample []byte) bool {
	if len(sample) == 0 {
		return true
	}
	return !bytes.ContainsRune(sample, 0) && utf8.Valid(sample)
}

func classifyDocumentKind(name string, mimeType string, sample []byte) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	mimeType = normalizeDocumentMIME(mimeType)
	isText := documentSampleIsText(sample)

	// Known MIME/signature results are authoritative. OpenXML is the one
	// deliberate exception: it is a ZIP container, so a matching Office
	// extension must win over the generic application/zip signature.
	if strings.HasPrefix(mimeType, "application/vnd.openxmlformats-officedocument.") ||
		mimeType == "application/msword" || strings.HasPrefix(mimeType, "application/vnd.ms-") {
		return documentKindOffice
	}
	if mimeType == "application/pdf" {
		return documentKindPDF
	}
	if strings.HasPrefix(mimeType, "image/") {
		return documentKindImage
	}
	if mimeType == "text/html" || mimeType == "application/xhtml+xml" {
		return documentKindHTML
	}
	if mimeType == "text/markdown" {
		return documentKindMarkdown
	}
	if strings.HasPrefix(mimeType, "audio/") {
		return documentKindAudio
	}
	if strings.HasPrefix(mimeType, "video/") {
		return documentKindVideo
	}

	if documentOfficeExtensions[ext] && !isText &&
		(mimeType == "application/zip" || mimeType == "application/octet-stream") {
		return documentKindOffice
	}
	if ext == ".pdf" && !isText {
		return documentKindPDF
	}
	if documentImageExtensions[ext] && (!isText || ext == ".svg") {
		return documentKindImage
	}
	if (ext == ".html" || ext == ".htm" || ext == ".xhtml") && isText {
		return documentKindHTML
	}
	if (ext == ".md" || ext == ".markdown" || ext == ".mdx") && isText {
		return documentKindMarkdown
	}
	if documentAudioExtensions[ext] && !isText {
		return documentKindAudio
	}
	if documentVideoExtensions[ext] && !isText {
		return documentKindVideo
	}
	if (documentArchiveExtensions[ext] && !isText) || mimeType == "application/zip" ||
		mimeType == "application/x-7z-compressed" || mimeType == "application/x-rar-compressed" {
		return documentKindArchive
	}
	if (documentCodeExtensions[ext] && isText) || strings.Contains(mimeType, "json") || strings.Contains(mimeType, "xml") ||
		strings.Contains(mimeType, "javascript") || strings.Contains(mimeType, "yaml") {
		return documentKindCode
	}
	if ((ext == ".txt" || ext == ".log" || ext == ".csv" || ext == ".tsv") && isText) ||
		(strings.HasPrefix(mimeType, "text/") && isText) {
		return documentKindText
	}
	if isText {
		return documentKindText
	}
	return documentKindBinary
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
	if err != nil || len(sample) == 0 {
		return "application/octet-stream"
	}
	detected := normalizeDocumentMIME(http.DetectContentType(sample))
	if strings.EqualFold(filepath.Ext(path), ".svg") && documentSampleIsText(sample) &&
		strings.Contains(strings.ToLower(string(sample)), "<svg") {
		return "image/svg+xml"
	}
	return detected
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
