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
	"agent-platform/internal/pathutil"
	"agent-platform/internal/rootpaths"
)

type proxyReferenceOptions struct {
	ChatID          string
	RunID           string
	Subject         string
	ResourceBaseURL string
	WorkspaceRoot   string
	References      []api.Reference
	Files           []string
}

func prepareProxyReferences(store chat.Store, ticketService *ResourceTicketService, options proxyReferenceOptions) ([]api.Reference, error) {
	out := make([]api.Reference, 0, len(options.References)+len(options.Files))
	for _, ref := range options.References {
		if resourceFileParamForChat(options.ChatID, ref.URL) == "" && strings.TrimSpace(ref.Path) != "" {
			if ref.Path == "/workspace" || strings.HasPrefix(ref.Path, "/workspace/") {
				return nil, fmt.Errorf("legacy path-only /workspace references are not accepted; re-materialize the file through the resource API")
			}
			materialized, err := materializeProxyFileReference(store, options.ChatID, options.RunID, options.WorkspaceRoot, ref.Path)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(ref.ID) != "" {
				materialized.ID = ref.ID
			}
			if strings.TrimSpace(ref.Type) != "" {
				materialized.Type = ref.Type
			}
			if strings.TrimSpace(ref.Name) != "" {
				materialized.Name = ref.Name
			}
			if strings.TrimSpace(ref.MimeType) != "" {
				materialized.MimeType = ref.MimeType
			}
			ref = materialized
		}
		ref = normalizeProxyReferencePath(ref, options.ChatID)
		out = append(out, normalizeProxyReferenceURL(ref, ticketService, options))
	}
	for _, file := range options.Files {
		ref, err := materializeProxyFileReference(store, options.ChatID, options.RunID, options.WorkspaceRoot, file)
		if err != nil {
			return nil, err
		}
		out = append(out, normalizeProxyReferenceURL(ref, ticketService, options))
	}
	return out, nil
}

func materializeProxyFileReference(store chat.Store, chatID string, runID string, workspaceRoot string, rawPath string) (api.Reference, error) {
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
	canonicalChatDir, err := pathutil.Canonicalize(chatDir)
	if err != nil {
		return api.Reference{}, fmt.Errorf("resolve proxy chat directory: %w", err)
	}
	chatDir = canonicalChatDir.Host
	sourcePath, err := resolveProxyFileSource(store, chatID, chatDir, workspaceRoot, rawPath)
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
	}

	relativePath = filepath.ToSlash(relativePath)
	name := filepath.Base(targetPath)
	size := info.Size()
	if targetInfo, err := os.Stat(targetPath); err == nil {
		size = targetInfo.Size()
	}
	return api.Reference{
		ID:        "proxy_file:" + strings.Trim(filepath.ToSlash(relativePath), "/"),
		Type:      "file",
		Name:      name,
		Path:      "",
		MimeType:  guessProxyMimeType(name),
		SizeBytes: &size,
		URL:       resourceURLForFileParam(filepath.ToSlash(filepath.Join(chatID, relativePath))),
		SHA256:    sha256FileHex(targetPath),
	}, nil
}

func resolveProxyFileSource(store chat.Store, chatID string, chatDir string, workspaceRoot string, rawPath string) (string, error) {
	if fileParam := resourceFileParam(rawPath); fileParam != "" {
		if store == nil {
			return "", fmt.Errorf("resource store unavailable")
		}
		sourcePath, err := store.ResolveResource(fileParam)
		if err != nil {
			return "", err
		}
		return sourcePath, nil
	}

	semanticRoots, err := rootpaths.New(workspaceRoot, filepath.Dir(chatDir), chatDir)
	if err != nil {
		return "", fmt.Errorf("resolve proxy roots: %w", err)
	}
	if rawPath == "/chat" || strings.HasPrefix(rawPath, "/chat/") ||
		rawPath == "@chat" || strings.HasPrefix(rawPath, "@chat/") {
		suffix := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(rawPath, "/chat"), "@chat"), "/")
		zone, sourcePath, err := semanticRoots.Classify(filepath.Join(chatDir, filepath.FromSlash(suffix)))
		if err != nil || zone != rootpaths.ZoneCurrentChat {
			return "", fmt.Errorf("proxy file escapes chat: %s", rawPath)
		}
		return sourcePath.Host, nil
	}
	if rawPath == "/workspace" || strings.HasPrefix(rawPath, "/workspace/") ||
		rawPath == "@workspace" || strings.HasPrefix(rawPath, "@workspace/") {
		if strings.TrimSpace(workspaceRoot) == "" {
			return "", fmt.Errorf("workspace_unavailable: proxy workspace file requires a workspace")
		}
		suffix := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(rawPath, "/workspace"), "@workspace"), "/")
		zone, sourcePath, err := semanticRoots.Classify(filepath.Join(workspaceRoot, filepath.FromSlash(suffix)))
		if err != nil {
			return "", fmt.Errorf("proxy file escapes workspace: %s", rawPath)
		}
		if zone == rootpaths.ZoneCurrentChat || zone == rootpaths.ZoneOtherChat {
			return "", fmt.Errorf("path_crosses_chat_root: proxy workspace file must use @chat for the current chat")
		}
		if zone != rootpaths.ZoneWorkspace {
			return "", fmt.Errorf("proxy file escapes workspace: %s", rawPath)
		}
		return sourcePath.Host, nil
	}

	if !filepath.IsAbs(rawPath) {
		if strings.TrimSpace(workspaceRoot) == "" {
			return "", fmt.Errorf("workspace_unavailable: relative proxy file requires a workspace")
		}
		zone, sourcePath, err := semanticRoots.Classify(filepath.Join(workspaceRoot, rawPath))
		if err != nil {
			return "", fmt.Errorf("proxy file escapes workspace: %s", rawPath)
		}
		if zone == rootpaths.ZoneCurrentChat || zone == rootpaths.ZoneOtherChat {
			return "", fmt.Errorf("path_crosses_chat_root: relative proxy file must not enter the chats root")
		}
		if zone != rootpaths.ZoneWorkspace {
			return "", fmt.Errorf("proxy file escapes workspace: %s", rawPath)
		}
		return sourcePath.Host, nil
	}

	zone, sourcePath, err := semanticRoots.Classify(rawPath)
	if err != nil {
		return "", err
	}
	switch zone {
	case rootpaths.ZoneCurrentChat, rootpaths.ZoneWorkspace:
		return sourcePath.Host, nil
	case rootpaths.ZoneOtherChat:
		return "", fmt.Errorf("proxy file belongs to another chat: %s", rawPath)
	default:
		return "", fmt.Errorf("proxy file must be under the current workspace or chat: %s", rawPath)
	}
}

func normalizeProxyReferencePath(ref api.Reference, chatID string) api.Reference {
	if resourceFileParamForChat(chatID, ref.URL) != "" {
		ref.Path = ""
	}
	return ref
}

func normalizeProxyReferenceURL(ref api.Reference, ticketService *ResourceTicketService, options proxyReferenceOptions) api.Reference {
	rawURL := strings.TrimSpace(ref.URL)
	if rawURL == "" {
		return ref
	}
	fileParam := resourceFileParamForChat(options.ChatID, rawURL)
	if fileParam == "" {
		return ref
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ref
	}
	if !isResourceURL(parsed, ref.URL) {
		parsed, err = url.Parse(resourceURLForFileParam(fileParam))
		if err != nil {
			return ref
		}
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
	raw := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if isResourceURL(parsed, rawURL) {
		return parsed.Query().Get("file")
	}
	if raw == "@chat" || strings.HasPrefix(raw, "@chat/") ||
		raw == "@workspace" || strings.HasPrefix(raw, "@workspace/") ||
		raw == "/chat" || strings.HasPrefix(raw, "/chat/") ||
		raw == "/workspace" || strings.HasPrefix(raw, "/workspace/") ||
		strings.HasPrefix(raw, "/") || strings.Contains(raw, `\`) {
		return ""
	}
	chatID, relativePath, err := chat.ParseResourceKey(raw)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(filepath.Join(chatID, relativePath))
}

// resourceFileParamForChat resolves a public ChatScope URL against its
// current Chat. Public reference URLs deliberately omit chatId; only the HTTP
// /api/resource data plane carries a full <chatId>/<relativePath> key.
func resourceFileParamForChat(chatID string, rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(raw)
	if err != nil || raw == "" {
		return ""
	}
	if parsed.IsAbs() || isResourceURL(parsed, rawURL) {
		return resourceFileParam(rawURL)
	}
	if parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		raw == "@chat" || strings.HasPrefix(raw, "@chat/") ||
		raw == "@workspace" || strings.HasPrefix(raw, "@workspace/") ||
		raw == "/chat" || strings.HasPrefix(raw, "/chat/") ||
		raw == "/workspace" || strings.HasPrefix(raw, "/workspace/") ||
		strings.HasPrefix(raw, "/") || strings.Contains(raw, `\`) {
		return ""
	}
	fileParam, err := chat.BuildResourceKey(chatID, parsed.Path)
	if err != nil {
		return ""
	}
	return fileParam
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
