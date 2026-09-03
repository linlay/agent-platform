package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/rootpaths"
	"agent-platform/internal/temppaths"
)

const uploadManifestName = ".uploads.jsonl"

type uploadManifestEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	CreatedAt int64  `json:"createdAt,omitempty"`
}

func (s *Server) handleViewport(w http.ResponseWriter, r *http.Request) {
	viewportKey := r.URL.Query().Get("viewportKey")
	if viewportKey == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "viewportKey is required"))
		return
	}
	payload, err := s.deps.Viewport.Get(r.Context(), viewportKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(payload))
}

func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	fileParam := r.URL.Query().Get("file")
	if fileParam == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "file is required"))
		return
	}
	if filepath.IsAbs(fileParam) {
		s.handleAbsoluteResource(w, r, fileParam)
		return
	}
	if chat.IsToolInternalPath(fileParam) || chat.IsBTWInternalPath(fileParam) {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	chatID, relativePath, parseErr := chat.ParseResourceKey(fileParam)
	if parseErr != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid resource key"))
		return
	}
	principal := PrincipalFromContext(r.Context())
	if s.deps.Config.ResourceTicket.Enabled() {
		ticket := strings.TrimSpace(r.URL.Query().Get("t"))
		if principal == nil {
			if ticket == "" {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource ticket required"))
				return
			}
			ticketChatID, err := s.ticketService.Verify(ticket)
			if err != nil {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, err.Error()))
				return
			}
			if ticketChatID != chatID {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource ticket chat mismatch"))
				return
			}
		}
	}
	if principal != nil && !s.principalCanAccessResourceChat(principal, chatID) {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	path, err := s.resolveResourcePath(chatID, relativePath, fileParam)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "resource not found"))
			return
		}
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	s.serveResourcePath(w, r, path, relativePath)
}

func (s *Server) handleAbsoluteResource(w http.ResponseWriter, r *http.Request, rawPath string) {
	chatID := strings.TrimSpace(r.URL.Query().Get("chatId"))
	if !chat.ValidChatID(chatID) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required for absolute resource paths"))
		return
	}
	principal := PrincipalFromContext(r.Context())
	if principal == nil || strings.TrimSpace(principal.Subject) == "" {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "authenticated principal required for absolute resource paths"))
		return
	}
	if !s.principalCanAccessResourceChat(principal, chatID) {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	agentKey, teamID, ok := s.resourceChatOwner(chatID)
	if !ok || strings.TrimSpace(teamID) != "" || strings.TrimSpace(agentKey) == "" {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "absolute resource paths are unavailable for this chat"))
		return
	}
	cleanPath := filepath.Clean(strings.TrimSpace(rawPath))
	if !filepath.IsAbs(cleanPath) || strings.ContainsRune(cleanPath, 0) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid absolute resource path"))
		return
	}
	chatRoots, chatRootsErr := rootpaths.New("", s.deps.Config.Paths.ChatsDir, s.deps.Chats.ChatDir(chatID))
	if chatRootsErr != nil {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	chatZone, _, chatClassifyErr := chatRoots.Classify(cleanPath)
	if chatClassifyErr != nil || pathWithinBase(cleanPath, s.deps.Config.Paths.ChatsDir) ||
		chatZone == rootpaths.ZoneCurrentChat || chatZone == rootpaths.ZoneOtherChat {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "absolute Chat resource paths are unavailable; use a ChatScope resource key"))
		return
	}
	tempState, tempPath, _, tempErr := temppaths.System().Classify(cleanPath)
	if tempErr != nil {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	if tempState == temppaths.Escape {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	if tempState == temppaths.Inside {
		s.serveResourcePath(w, r, tempPath.Host, cleanPath)
		return
	}
	if s.deps.Registry == nil {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "absolute resource path is outside the current workspace"))
		return
	}
	agentDef, ok := s.deps.Registry.AgentDefinition(agentKey)
	if !ok || strings.TrimSpace(agentDef.Workspace.Root) == "" {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "absolute resource path is outside the current workspace"))
		return
	}
	roots, err := rootpaths.New(agentDef.Workspace.Root, s.deps.Config.Paths.ChatsDir, s.deps.Chats.ChatDir(chatID))
	if err != nil {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource access denied"))
		return
	}
	zone, candidate, err := roots.Classify(cleanPath)
	if err != nil || zone != rootpaths.ZoneWorkspace {
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "absolute resource path is outside the current workspace"))
		return
	}
	s.serveResourcePath(w, r, candidate.Host, cleanPath)
}

func (s *Server) resourceChatOwner(chatID string) (string, string, bool) {
	if summary, err := s.deps.Chats.Summary(chatID); err == nil && summary != nil {
		return strings.TrimSpace(summary.AgentKey), strings.TrimSpace(summary.TeamID), true
	}
	if s.deps.Archives == nil {
		return "", "", false
	}
	archived, err := s.deps.Archives.LoadArchived(chatID)
	if err != nil || archived == nil {
		return "", "", false
	}
	return strings.TrimSpace(archived.Summary.AgentKey), strings.TrimSpace(archived.Summary.TeamID), true
}

func (s *Server) serveResourcePath(w http.ResponseWriter, r *http.Request, path string, semanticName string) {
	file, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "resource not found"))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "resource not found"))
		return
	}
	semanticName = strings.TrimSpace(semanticName)
	if semanticName == "" {
		semanticName = info.Name()
	}
	detectedMIME := resourceContentType(semanticName, file)
	sample := make([]byte, 512)
	n, _ := file.ReadAt(sample, 0)
	metadata := resolveDocumentMetadata(semanticName, detectedMIME, sample[:n])
	w.Header().Set("Content-Type", metadata.MIMEType)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-ZenMind-Resource-Revision", fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixMilli()))
	w.Header().Set("X-ZenMind-Document-Kind", metadata.DocumentKind)
	disposition := ""
	if resourceDownloadRequested(r) {
		disposition = "attachment"
	} else if strings.HasPrefix(strings.ToLower(metadata.MIMEType), "image/") {
		disposition = "inline"
	}
	if disposition != "" {
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filepath.Base(semanticName)}))
	}
	if strings.HasPrefix(strings.ToLower(metadata.MIMEType), "image/svg+xml") {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	http.ServeContent(w, r, filepath.Base(semanticName), info.ModTime(), file)
}

func (s *Server) resolveResourcePath(chatID string, relativePath string, originalKey string) (string, error) {
	path, err := s.deps.Chats.ResolveResource(originalKey)
	if err == nil || !errors.Is(err, os.ErrNotExist) || s.deps.Archives == nil {
		return path, err
	}
	return s.deps.Archives.ResolveResource(chatID, relativePath)
}

func (s *Server) principalCanAccessResourceChat(principal *Principal, chatID string) bool {
	if principal == nil || strings.TrimSpace(principal.Subject) == "" {
		return true
	}
	if summary, err := s.deps.Chats.Summary(chatID); err == nil && summary != nil {
		return queryPrincipalCanReferenceChat(WithPrincipal(context.Background(), principal), *summary)
	}
	if s.deps.Archives == nil {
		return false
	}
	archived, err := s.deps.Archives.LoadArchived(chatID)
	if err != nil || archived == nil {
		return false
	}
	summary := chat.Summary{ChatID: archived.Summary.ChatID, Source: archived.Summary.Source}
	return queryPrincipalCanReferenceChat(WithPrincipal(context.Background(), principal), summary)
}

func resourceContentType(filename string, file *os.File) string {
	buffer := make([]byte, 512)
	n, _ := file.Read(buffer)
	_, _ = file.Seek(0, io.SeekStart)
	if strings.EqualFold(filepath.Ext(filename), ".svg") && documentSampleIsText(buffer[:n]) &&
		strings.Contains(strings.ToLower(string(buffer[:n])), "<svg") {
		return "image/svg+xml"
	}
	return http.DetectContentType(buffer[:n])
}

func resourceDownloadRequested(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("download"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func (s *Server) handleToolResult(w http.ResponseWriter, r *http.Request) {
	chatID := strings.TrimSpace(r.URL.Query().Get("chatId"))
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	if !chat.ValidChatID(chatID) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "chatId is required"))
		return
	}
	if !chat.IsToolResultRelativePath(relPath) {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid tool result path"))
		return
	}
	if s.deps.Config.ResourceTicket.Enabled() {
		principal := PrincipalFromContext(r.Context())
		ticket := strings.TrimSpace(r.URL.Query().Get("t"))
		if principal == nil {
			if ticket == "" {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource ticket required"))
				return
			}
			ticketChatID, err := s.ticketService.Verify(ticket)
			if err != nil {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, err.Error()))
				return
			}
			if ticketChatID != chatID {
				writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "resource ticket chat mismatch"))
				return
			}
		}
	}
	path, err := s.resolveToolResultPath(chatID, relPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "tool result not found"))
			return
		}
		writeJSON(w, http.StatusForbidden, api.Failure(http.StatusForbidden, "tool result access denied"))
		return
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "tool result not found"))
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		writeJSON(w, http.StatusNotFound, api.Failure(http.StatusNotFound, "tool result not found"))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

func (s *Server) resolveToolResultPath(chatID string, relPath string) (string, error) {
	if !chat.ValidChatID(chatID) || !chat.IsToolResultRelativePath(relPath) {
		return "", os.ErrPermission
	}
	clean := filepath.Clean(relPath)
	if path, err := resolveToolResultPathInChatDir(s.deps.Chats.ChatDir(chatID), clean); err == nil {
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if s.deps.Archives != nil {
		return resolveToolResultPathInChatDir(s.deps.Archives.ChatDir(chatID), clean)
	}
	return "", os.ErrNotExist
}

func resolveToolResultPathInChatDir(chatDir string, relPath string) (string, error) {
	if strings.TrimSpace(chatDir) == "" || !chat.IsToolResultRelativePath(relPath) {
		return "", os.ErrPermission
	}
	base := filepath.Clean(chatDir)
	path := filepath.Join(base, filepath.Clean(relPath))
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", os.ErrPermission
	}
	if !chat.IsToolResultRelativePath(rel) {
		return "", os.ErrPermission
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", os.ErrNotExist
	}
	return path, nil
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid multipart form"))
		return
	}
	requestID := strings.TrimSpace(r.FormValue("requestId"))
	if requestID == "" {
		requestID = newRunID()
	}
	chatID := strings.TrimSpace(r.FormValue("chatId"))
	if chatID == "" {
		chatID = newChatID()
	}
	agentKey := strings.TrimSpace(r.FormValue("agentKey"))
	source := ""
	if principal := PrincipalFromContext(r.Context()); principal != nil && strings.TrimSpace(principal.Subject) != "" {
		source = api.ChatSourceQueryPrefix + strings.TrimSpace(principal.Subject)
	}
	summary, created, err := s.deps.Chats.EnsureChatWithSource(chatID, agentKey, "", r.FormValue("name"), source)
	if err != nil {
		if isTimeContractViolation(err) {
			writeTimeContractViolation(w, err)
			return
		}
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	if created {
		s.broadcast("chat.created", chatCreatedPayload(chatID, summary.ChatName, agentKey, summary.CreatedAt, summary.Source))
	}
	file, header, err := pickUploadFile(r.MultipartForm)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	defer file.Close()

	targetName := safeFilename(header.Filename)
	uploadID, err := s.allocateUploadID(chatID, targetName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}
	referenceRelativePath := targetName
	targetPath := filepath.Join(s.deps.Chats.ChatDir(chatID), targetName)
	sum, size, err := saveUploadedFile(targetPath, file)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	resourceURL, err := chat.BuildChatScopeRef(referenceRelativePath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "failed to create upload resource reference"))
		return
	}
	referencePath := s.uploadReferencePath(referenceRelativePath, targetPath, agentKey)
	writeJSON(w, http.StatusOK, api.Success(api.UploadResponse{
		RequestID: requestID,
		ChatID:    chatID,
		Upload: api.UploadTicket{
			ID:        uploadID,
			Type:      "file",
			Name:      targetName,
			Path:      referencePath,
			MimeType:  header.Header.Get("Content-Type"),
			SizeBytes: size,
			URL:       resourceURL,
			SHA256:    sum,
		},
	}))
}

func (s *Server) uploadReferencePath(relativePath string, targetPath string, agentKey string) string {
	if s.agentUsesContainerHubForKey(agentKey) {
		return "/chat/" + filepath.ToSlash(relativePath)
	}
	if abs, err := filepath.Abs(targetPath); err == nil {
		return abs
	}
	return filepath.Clean(targetPath)
}

func (s *Server) agentUsesContainerHubForKey(agentKey string) bool {
	if s == nil || s.deps.Config.IsLocalMode() {
		return false
	}
	if strings.TrimSpace(agentKey) == "" || s.deps.Registry == nil {
		return false
	}
	if def, ok := s.deps.Registry.AgentDefinition(agentKey); ok {
		return s.agentUsesContainerHub(def)
	}
	return false
}

func (s *Server) agentUsesContainerHub(def catalog.AgentDefinition) bool {
	if s == nil || s.deps.Config.IsLocalMode() {
		return false
	}
	return s.deps.Config.ContainerHub.Enabled && hasRuntimeSandbox(def.Runtime)
}

func pickUploadFile(form *multipart.Form) (multipart.File, *multipart.FileHeader, error) {
	if form == nil || len(form.File) == 0 {
		return nil, nil, errors.New("file is required")
	}
	for _, headers := range form.File {
		if len(headers) == 0 {
			continue
		}
		file, err := headers[0].Open()
		return file, headers[0], err
	}
	return nil, nil, errors.New("file is required")
}

func saveUploadedFile(path string, src multipart.File) (string, int64, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", 0, err
	}
	file, err := os.Create(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	hash := sha256.New()
	writer := io.MultiWriter(file, hash)
	size, err := io.Copy(writer, src)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func (s *Server) allocateUploadID(chatID string, name string) (string, error) {
	s.uploadMu.Lock()
	defer s.uploadMu.Unlock()

	chatDir := s.deps.Chats.ChatDir(chatID)
	next, err := nextUploadSequence(chatDir)
	if err != nil {
		return "", err
	}
	entry := uploadManifestEntry{
		ID:        fmt.Sprintf("r%02d", next),
		Name:      name,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := appendUploadManifestEntry(chatDir, entry); err != nil {
		return "", err
	}
	return entry.ID, nil
}

func nextUploadSequence(chatDir string) (int, error) {
	manifestPath := filepath.Join(chatDir, uploadManifestName)
	if _, err := os.Stat(manifestPath); err == nil {
		maxID, err := maxUploadSequenceFromManifest(manifestPath)
		if err != nil {
			return 0, err
		}
		return maxID + 1, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}

	count, err := countExistingRootUploads(chatDir)
	if err != nil {
		return 0, err
	}
	return count + 1, nil
}

func maxUploadSequenceFromManifest(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	maxID := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry uploadManifestEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return 0, err
		}
		if sequence := uploadIDSequence(entry.ID); sequence > maxID {
			maxID = sequence
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return maxID, nil
}

func uploadIDSequence(id string) int {
	if !strings.HasPrefix(id, "r") {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimPrefix(id, "r"))
	if err != nil || value < 1 {
		return 0
	}
	return value
}

func countExistingRootUploads(chatDir string) (int, error) {
	entries, err := os.ReadDir(chatDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || isUploadMetadataFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		if info.Mode().IsRegular() {
			count++
		}
	}
	return count, nil
}

func isUploadMetadataFile(name string) bool {
	switch name {
	case uploadManifestName:
		return true
	default:
		return false
	}
}

func appendUploadManifestEntry(chatDir string, entry uploadManifestEntry) error {
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(chatDir, uploadManifestName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func safeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "upload.bin"
	}
	return name
}
