package server

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/filetools"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/runtimeenv"
	"agent-platform/internal/textcodec"
)

type resolvedAgentFile struct {
	AgentKey      string
	WorkspaceRoot string
	RequestedPath string
	Path          string
	AbsolutePath  string
	Info          os.FileInfo
}

func (s *Server) handleAgentFile(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	responseMode := strings.TrimSpace(query.Get("response"))
	if responseMode != "" && !strings.EqualFold(responseMode, "json") && !strings.EqualFold(responseMode, "content") {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "response must be content or json"))
		return
	}
	resolved, err := s.resolveAgentFile(query.Get("agentKey"), query.Get("path"))
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	if strings.EqualFold(responseMode, "content") {
		s.serveAgentFileContent(w, r, resolved)
		return
	}
	result, err := s.readAgentFileMetadata(resolved, query.Get("encoding"))
	s.writeAgentHTTPResponse(w, result, err)
}

func (s *Server) resolveAgentFile(agentKey string, requestedPath string) (resolvedAgentFile, error) {
	if s.deps.Registry == nil {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent registry is not configured")
	}
	agentKey = strings.TrimSpace(agentKey)
	requestedPath = strings.TrimSpace(requestedPath)
	if agentKey == "" {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agentKey is required")
	}
	if requestedPath == "" {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "path is required")
	}
	if strings.ContainsRune(requestedPath, 0) {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "path contains invalid character")
	}
	def, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusNotFound, "not_found", "agent not found")
	}
	workspaceRoot := strings.TrimSpace(def.Workspace.Root)
	if workspaceRoot == "" || strings.EqualFold(workspaceRoot, catalog.AgentWorkspaceRootChat) {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agent workspace is not a stable directory")
	}
	workspaceRoot = filepath.Clean(pathutil.ExpandHome(workspaceRoot))
	if !filepath.IsAbs(workspaceRoot) {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agent workspace must be an absolute path")
	}
	info, err := os.Stat(workspaceRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return resolvedAgentFile{}, newAgentStatusError(http.StatusNotFound, "not_found", "workspace directory not found")
		}
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if !info.IsDir() {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "agent workspace is not a directory")
	}
	workspaceCanonical, err := pathutil.Canonicalize(workspaceRoot)
	if err != nil {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}

	candidate := filepath.FromSlash(requestedPath)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspaceCanonical.Host, candidate)
	}
	candidate = filepath.Clean(candidate)
	targetCanonical, err := pathutil.Canonicalize(candidate)
	if err != nil {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if !pathutil.WithinRoot(targetCanonical, workspaceCanonical) {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusForbidden, "forbidden", "file path is outside agent workspace")
	}
	if filetools.IsBlockedDeviceFile(targetCanonical.Host) {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusForbidden, "forbidden", "device file is blocked")
	}
	fileInfo, err := os.Stat(targetCanonical.Host)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return resolvedAgentFile{}, newAgentStatusError(http.StatusNotFound, "not_found", "file not found")
		}
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if fileInfo.IsDir() {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "path is a directory")
	}
	if !fileInfo.Mode().IsRegular() {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "path is not a regular file")
	}
	relPath, err := filepath.Rel(workspaceCanonical.Host, targetCanonical.Host)
	if err != nil {
		return resolvedAgentFile{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	return resolvedAgentFile{
		AgentKey:      agentKey,
		WorkspaceRoot: workspaceCanonical.Host,
		RequestedPath: requestedPath,
		Path:          filepath.ToSlash(filepath.Clean(relPath)),
		AbsolutePath:  targetCanonical.Host,
		Info:          fileInfo,
	}, nil
}

func (s *Server) readAgentFileMetadata(resolved resolvedAgentFile, requestedEncoding string) (api.AgentFileResponse, error) {
	response := baseAgentFileResponse(resolved)
	response.MimeType = detectAgentFileMIME(resolved.AbsolutePath)
	response.SHA256 = sha256FileHex(resolved.AbsolutePath)
	response.ContentURL = agentFileContentURL(resolved.AgentKey, resolved.RequestedPath)

	maxBytes := s.deps.Config.FileTools.MaxReadBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	data, truncated, err := readAgentFilePrefix(resolved.AbsolutePath, int64(maxBytes)+1)
	if err != nil {
		return api.AgentFileResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	if truncated {
		data = data[:maxBytes]
	}
	response.ReadBytes = len(data)
	response.Truncated = truncated

	if !filetools.IsBinaryExtension(resolved.AbsolutePath) {
		decoded, ok, decodeErr := textcodec.DecodeFileText(data, requestedEncoding, runtimeenv.Detect())
		if decodeErr != nil {
			return api.AgentFileResponse{}, newAgentStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", decodeErr.Error())
		}
		if ok {
			response.ContentKind = "text"
			response.Encoding = decoded.Encoding
			response.Content = decoded.Content
			return response, nil
		}
	}
	response.ContentKind = "binary"
	response.ReadBytes = 0
	return response, nil
}

func baseAgentFileResponse(resolved resolvedAgentFile) api.AgentFileResponse {
	return api.AgentFileResponse{
		AgentKey:       resolved.AgentKey,
		WorkspaceRoot:  resolved.WorkspaceRoot,
		RequestedPath:  resolved.RequestedPath,
		Path:           resolved.Path,
		AbsolutePath:   resolved.AbsolutePath,
		Name:           resolved.Info.Name(),
		Kind:           "file",
		ContentKind:    "binary",
		SizeBytes:      resolved.Info.Size(),
		ModifiedUnixMs: resolved.Info.ModTime().UnixMilli(),
	}
}

func (s *Server) serveAgentFileContent(w http.ResponseWriter, r *http.Request, resolved resolvedAgentFile) {
	file, err := os.Open(resolved.AbsolutePath)
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error()))
		return
	}
	defer file.Close()
	if contentType := detectAgentFileMIME(resolved.AbsolutePath); strings.TrimSpace(contentType) != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if disposition := mime.FormatMediaType("inline", map[string]string{"filename": resolved.Info.Name()}); disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	http.ServeContent(w, r, resolved.Info.Name(), resolved.Info.ModTime(), file)
}

func readAgentFilePrefix(path string, maxBytes int64) ([]byte, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	limited := &io.LimitedReader{R: file, N: maxBytes}
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	return data, limited.N <= 0, nil
}

func detectAgentFileMIME(path string) string {
	if value := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); strings.TrimSpace(value) != "" {
		return value
	}
	data, _, err := readAgentFilePrefix(path, 512)
	if err != nil || len(data) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(data)
}

func agentFileContentURL(agentKey string, requestedPath string) string {
	values := url.Values{}
	values.Set("agentKey", agentKey)
	values.Set("path", requestedPath)
	values.Set("response", "content")
	return "/api/file?" + values.Encode()
}
