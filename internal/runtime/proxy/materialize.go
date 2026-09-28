package proxy

import (
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/chat"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/rootpaths"
	"agent-platform/internal/runtime/session"
	runtimetypes "agent-platform/internal/runtime/types"
)

type ReferenceOptions struct {
	ChatID          string
	RunID           string
	Subject         string
	ResourceBaseURL string
	WorkspaceRoot   string
	References      []runtimetypes.Reference
	Files           []string
}

func PrepareReferences(store chat.Store, ticketService TicketIssuer, options ReferenceOptions) ([]runtimetypes.Reference, error) {
	out := make([]runtimetypes.Reference, 0, len(options.References)+len(options.Files))
	for _, ref := range options.References {
		if session.ResourceFileParamForChat(options.ChatID, ref.URL) == "" && strings.TrimSpace(ref.Path) != "" {
			if ref.Path == "/workspace" || strings.HasPrefix(ref.Path, "/workspace/") {
				return nil, fmt.Errorf("legacy path-only /workspace references are not accepted; re-materialize the file through the resource API")
			}
			materialized, err := MaterializeProxyFileReference(store, options.ChatID, options.RunID, options.WorkspaceRoot, ref.Path)
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
		ref = NormalizeProxyReferencePath(ref, options.ChatID)
		out = append(out, NormalizeProxyReferenceURL(ref, ticketService, options))
	}
	for _, file := range options.Files {
		ref, err := MaterializeProxyFileReference(store, options.ChatID, options.RunID, options.WorkspaceRoot, file)
		if err != nil {
			return nil, err
		}
		out = append(out, NormalizeProxyReferenceURL(ref, ticketService, options))
	}
	return out, nil
}

func MaterializeProxyFileReference(store chat.Store, chatID string, runID string, workspaceRoot string, rawPath string) (runtimetypes.Reference, error) {
	if store == nil {
		return runtimetypes.Reference{}, fmt.Errorf("chat store is unavailable")
	}
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return runtimetypes.Reference{}, fmt.Errorf("proxy file path is empty")
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return runtimetypes.Reference{}, fmt.Errorf("chatId is required for proxy file reference")
	}

	chatDir := store.ChatDir(chatID)
	canonicalChatDir, err := pathutil.Canonicalize(chatDir)
	if err != nil {
		return runtimetypes.Reference{}, fmt.Errorf("resolve proxy chat directory: %w", err)
	}
	chatDir = canonicalChatDir.Host
	sourcePath, err := ResolveProxyFileSource(store, chatDir, workspaceRoot, rawPath)
	if err != nil {
		return runtimetypes.Reference{}, err
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return runtimetypes.Reference{}, fmt.Errorf("proxy file not found %q: %w", rawPath, err)
	}
	if info.IsDir() {
		return runtimetypes.Reference{}, fmt.Errorf("proxy file is a directory: %s", rawPath)
	}

	targetPath := sourcePath
	relativePath, relErr := filepath.Rel(chatDir, targetPath)
	if relErr != nil || IsPathOutsideBase(relativePath) {
		targetDir := filepath.Join(chatDir, "proxy-inputs", SafePathSegment(runID, "run"))
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return runtimetypes.Reference{}, err
		}
		targetPath = DeduplicateProxyInputPath(targetDir, filepath.Base(sourcePath), sourcePath)
		if !SameFilesystemPath(sourcePath, targetPath) {
			if err := CopyProxyFile(sourcePath, targetPath); err != nil {
				return runtimetypes.Reference{}, err
			}
		}
		relativePath, relErr = filepath.Rel(chatDir, targetPath)
		if relErr != nil || IsPathOutsideBase(relativePath) {
			return runtimetypes.Reference{}, fmt.Errorf("proxy materialized file escaped chat dir: %s", targetPath)
		}
	}

	relativePath = filepath.ToSlash(relativePath)
	name := filepath.Base(targetPath)
	size := info.Size()
	if targetInfo, err := os.Stat(targetPath); err == nil {
		size = targetInfo.Size()
	}
	return runtimetypes.Reference{
		ID:        "proxy_file:" + strings.Trim(filepath.ToSlash(relativePath), "/"),
		Type:      "file",
		Name:      name,
		Path:      "",
		MimeType:  GuessProxyMimeType(name),
		SizeBytes: &size,
		URL:       ResourceURLForFileParam(filepath.ToSlash(filepath.Join(chatID, relativePath))),
		SHA256:    Sha256FileHex(targetPath),
	}, nil
}

func ResolveProxyFileSource(store chat.Store, chatDir string, workspaceRoot string, rawPath string) (string, error) {
	if fileParam := session.ResourceFileParam(rawPath); fileParam != "" {
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

func NormalizeProxyReferencePath(ref runtimetypes.Reference, chatID string) runtimetypes.Reference {
	if session.ResourceFileParamForChat(chatID, ref.URL) != "" {
		ref.Path = ""
	}
	return ref
}

func NormalizeProxyReferenceURL(ref runtimetypes.Reference, ticketService TicketIssuer, options ReferenceOptions) runtimetypes.Reference {
	rawURL := strings.TrimSpace(ref.URL)
	if rawURL == "" {
		return ref
	}
	fileParam := session.ResourceFileParamForChat(options.ChatID, rawURL)
	if fileParam == "" {
		return ref
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ref
	}
	if !isResourceURL(parsed, ref.URL) {
		parsed, err = url.Parse(ResourceURLForFileParam(fileParam))
		if err != nil {
			return ref
		}
	}
	base := strings.TrimRight(strings.TrimSpace(options.ResourceBaseURL), "/")
	if parsed.IsAbs() && !SameURLOrigin(parsed, base) {
		return ref
	}
	query := parsed.Query()
	if query.Get("file") != "" && query.Get("t") == "" && ticketService != nil && ticketService.Enabled() {
		subject := strings.TrimSpace(options.Subject)
		if subject == "" {
			subject = "proxy-agent"
		}
		if token := ticketService.Issue(subject, strings.SplitN(strings.TrimLeft(query.Get("file"), "/"), "/", 2)[0]); token != "" {
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

// session.ResourceFileParamForChat resolves a public ChatScope URL against its
// current Chat. Public reference URLs deliberately omit chatId; only the HTTP
// /api/resource data plane carries a full <chatId>/<relativePath> key.

func SameURLOrigin(parsed *url.URL, base string) bool {
	if parsed == nil || strings.TrimSpace(base) == "" {
		return false
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, baseURL.Scheme) && strings.EqualFold(parsed.Host, baseURL.Host)
}

func ResourceURLForFileParam(fileParam string) string {
	return "/api/resource?file=" + url.QueryEscape(filepath.ToSlash(fileParam))
}

func SafePathSegment(value string, fallback string) string {
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

func DeduplicateProxyInputPath(dir string, filename string, sourcePath string) string {
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
		if SameFileContent(sourcePath, candidate) {
			return candidate
		}
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func SameFileContent(left string, right string) bool {
	leftData, leftErr := os.ReadFile(left)
	rightData, rightErr := os.ReadFile(right)
	return leftErr == nil && rightErr == nil && string(leftData) == string(rightData)
}

func SameFilesystemPath(left string, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func CopyProxyFile(src string, dst string) error {
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

func GuessProxyMimeType(filename string) string {
	if value := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); value != "" {
		return value
	}
	return "application/octet-stream"
}

func Sha256FileHex(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func PathWithinBase(path string, base string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(path))
	if err != nil {
		return false
	}
	return !IsPathOutsideBase(rel)
}

func IsPathOutsideBase(rel string) bool {
	clean := filepath.Clean(rel)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator))
}

type TicketIssuer interface {
	Enabled() bool
	Issue(subject, chatID string) string
}
