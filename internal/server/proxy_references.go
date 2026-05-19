package server

import (
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
)

type proxyReferenceOptions struct {
	ChatID          string
	RunID           string
	Subject         string
	ResourceBaseURL string
	References      []api.Reference
	Files           []string
}

func prepareProxyReferences(store chat.Store, ticketService *ResourceTicketService, options proxyReferenceOptions) ([]api.Reference, error) {
	out := make([]api.Reference, 0, len(options.References)+len(options.Files))
	for _, ref := range options.References {
		out = append(out, normalizeProxyReferenceURL(ref, ticketService, options))
	}
	for _, file := range options.Files {
		ref, err := materializeProxyFileReference(store, options.ChatID, options.RunID, file)
		if err != nil {
			return nil, err
		}
		out = append(out, normalizeProxyReferenceURL(ref, ticketService, options))
	}
	return out, nil
}

func materializeProxyFileReference(store chat.Store, chatID string, runID string, rawPath string) (api.Reference, error) {
	if store == nil {
		return api.Reference{}, fmt.Errorf("chat store is unavailable")
	}
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return api.Reference{}, fmt.Errorf("proxy file path is empty")
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return api.Reference{}, fmt.Errorf("chatId is required for proxy file reference")
	}

	chatDir := store.ChatDir(chatID)
	sourcePath, sandboxPath, err := resolveProxyFileSource(store, chatID, chatDir, rawPath)
	if err != nil {
		return api.Reference{}, err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return api.Reference{}, fmt.Errorf("proxy file not found %q: %w", rawPath, err)
	}
	if info.IsDir() {
		return api.Reference{}, fmt.Errorf("proxy file is a directory: %s", rawPath)
	}

	targetPath := sourcePath
	relativePath, relErr := filepath.Rel(chatDir, targetPath)
	if relErr != nil || isPathOutsideBase(relativePath) {
		targetDir := filepath.Join(chatDir, "proxy-inputs", safePathSegment(runID, "run"))
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return api.Reference{}, err
		}
		targetPath = deduplicateProxyInputPath(targetDir, filepath.Base(sourcePath), sourcePath)
		if !sameFilesystemPath(sourcePath, targetPath) {
			if err := copyProxyFile(sourcePath, targetPath); err != nil {
				return api.Reference{}, err
			}
		}
		relativePath, relErr = filepath.Rel(chatDir, targetPath)
		if relErr != nil || isPathOutsideBase(relativePath) {
			return api.Reference{}, fmt.Errorf("proxy materialized file escaped chat dir: %s", targetPath)
		}
		if sandboxPath == "" || !strings.HasPrefix(sandboxPath, "/workspace/") {
			sandboxPath = "/workspace/" + filepath.ToSlash(relativePath)
		}
	}

	relativePath = filepath.ToSlash(relativePath)
	name := filepath.Base(targetPath)
	size := info.Size()
	if targetInfo, err := os.Stat(targetPath); err == nil {
		size = targetInfo.Size()
	}
	return api.Reference{
		ID:          "proxy_file:" + strings.Trim(filepath.ToSlash(relativePath), "/"),
		Type:        "file",
		Name:        name,
		MimeType:    guessProxyMimeType(name),
		SizeBytes:   &size,
		URL:         resourceURLForFileParam(filepath.ToSlash(filepath.Join(chatID, relativePath))),
		SHA256:      sha256FileHex(targetPath),
		SandboxPath: sandboxPath,
	}, nil
}

func resolveProxyFileSource(store chat.Store, chatID string, chatDir string, rawPath string) (string, string, error) {
	if fileParam := resourceFileParam(rawPath); fileParam != "" {
		sourcePath, err := store.ResolveResource(fileParam)
		if err != nil {
			return "", "", err
		}
		return sourcePath, sandboxPathForResourceFile(chatID, fileParam), nil
	}

	if strings.HasPrefix(rawPath, "/workspace") {
		suffix := strings.TrimLeft(strings.TrimPrefix(rawPath, "/workspace"), "/")
		sourcePath := filepath.Clean(filepath.Join(chatDir, suffix))
		if !pathWithinBase(sourcePath, chatDir) {
			return "", "", fmt.Errorf("proxy file escapes workspace: %s", rawPath)
		}
		return sourcePath, "/workspace/" + filepath.ToSlash(suffix), nil
	}

	if !filepath.IsAbs(rawPath) {
		sourcePath := filepath.Clean(filepath.Join(chatDir, rawPath))
		if !pathWithinBase(sourcePath, chatDir) {
			return "", "", fmt.Errorf("proxy file escapes chat dir: %s", rawPath)
		}
		return sourcePath, "/workspace/" + filepath.ToSlash(rawPath), nil
	}

	sourcePath := filepath.Clean(rawPath)
	if pathWithinBase(sourcePath, chatDir) {
		rel, _ := filepath.Rel(chatDir, sourcePath)
		return sourcePath, "/workspace/" + filepath.ToSlash(rel), nil
	}
	workingDir, err := os.Getwd()
	if err != nil || !pathWithinBase(sourcePath, workingDir) {
		return "", "", fmt.Errorf("proxy file must be under /workspace, current chat, or server workspace: %s", rawPath)
	}
	return sourcePath, rawPath, nil
}

func normalizeProxyReferenceURL(ref api.Reference, ticketService *ResourceTicketService, options proxyReferenceOptions) api.Reference {
	if strings.TrimSpace(ref.URL) == "" {
		return ref
	}
	parsed, err := url.Parse(strings.TrimSpace(ref.URL))
	if err != nil {
		return ref
	}
	if !isResourceURL(parsed, ref.URL) {
		return ref
	}
	base := strings.TrimRight(strings.TrimSpace(options.ResourceBaseURL), "/")
	if parsed.IsAbs() && !sameURLOrigin(parsed, base) {
		return ref
	}
	query := parsed.Query()
	if query.Get("file") != "" && query.Get("t") == "" && ticketService != nil && ticketService.cfg.Enabled() {
		subject := strings.TrimSpace(options.Subject)
		if subject == "" {
			subject = "proxy-agent"
		}
		if token := ticketService.Issue(subject, resourceChatID(query.Get("file"))); token != "" {
			query.Set("t", token)
		}
	}
	parsed.RawQuery = query.Encode()
	if parsed.IsAbs() {
		ref.URL = parsed.String()
		return ref
	}
	if base == "" {
		ref.URL = parsed.String()
		return ref
	}
	ref.URL = base + parsed.String()
	return ref
}

func requestBaseURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return ""
	}
	proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	return strings.TrimRight(proto+"://"+host, "/")
}

func resourceFileParam(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	if !isResourceURL(parsed, rawURL) {
		return ""
	}
	return parsed.Query().Get("file")
}

func isResourceURL(parsed *url.URL, rawURL string) bool {
	if parsed == nil {
		return false
	}
	if parsed.Path == "" && strings.HasPrefix(strings.TrimSpace(rawURL), "/api/resource") {
		return true
	}
	return parsed.Path == "/api/resource" || strings.HasSuffix(parsed.Path, "/api/resource")
}

func sameURLOrigin(parsed *url.URL, base string) bool {
	if parsed == nil || strings.TrimSpace(base) == "" {
		return false
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, baseURL.Scheme) && strings.EqualFold(parsed.Host, baseURL.Host)
}

func resourceURLForFileParam(fileParam string) string {
	return "/api/resource?file=" + url.QueryEscape(filepath.ToSlash(fileParam))
}

func sandboxPathForResourceFile(chatID string, fileParam string) string {
	clean := filepath.ToSlash(filepath.Clean(fileParam))
	prefix := strings.TrimSpace(chatID) + "/"
	if strings.HasPrefix(clean, prefix) {
		return "/workspace/" + strings.TrimPrefix(clean, prefix)
	}
	return ""
}

func safePathSegment(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	value = strings.ReplaceAll(value, "\\", "_")
	value = strings.ReplaceAll(value, "/", "_")
	if value == "." || value == ".." {
		return fallback
	}
	return value
}

func deduplicateProxyInputPath(dir string, filename string, sourcePath string) string {
	if strings.TrimSpace(filename) == "" || filename == "." || filename == string(filepath.Separator) {
		filename = "file"
	}
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	if base == "" {
		base = "file"
	}
	for index := 0; ; index++ {
		candidateName := filename
		if index > 0 {
			candidateName = fmt.Sprintf("%s-%d%s", base, index, ext)
		}
		candidate := filepath.Join(dir, candidateName)
		if sameFileContent(sourcePath, candidate) {
			return candidate
		}
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func sameFileContent(left string, right string) bool {
	leftData, leftErr := os.ReadFile(left)
	rightData, rightErr := os.ReadFile(right)
	return leftErr == nil && rightErr == nil && string(leftData) == string(rightData)
}

func sameFilesystemPath(left string, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func copyProxyFile(src string, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func guessProxyMimeType(filename string) string {
	if value := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); value != "" {
		return value
	}
	return "application/octet-stream"
}

func sha256FileHex(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func pathWithinBase(path string, base string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	if err != nil {
		return false
	}
	return !isPathOutsideBase(rel)
}

func isPathOutsideBase(rel string) bool {
	clean := filepath.Clean(rel)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator))
}
