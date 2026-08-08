package project

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/filetools"
	"agent-platform/internal/pathutil"
	"agent-platform/internal/rootpaths"
	"agent-platform/internal/runtimeenv"
	"agent-platform/internal/textcodec"
)

const (
	DefaultPageLimit = 200
	MaxPageLimit     = 1000
)

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e Error) Error() string { return e.Message }

type Service struct {
	Registry     catalog.Registry
	Chats        chat.Store
	History      contracts.ProjectFileHistoryReader
	ChatsRoot    string
	MaxReadBytes int
}

type workspace struct {
	definition catalog.AgentDefinition
	roots      rootpaths.Roots
}

type pageCursor struct {
	Revision string `json:"revision"`
	Offset   int    `json:"offset"`
}

func (s Service) Tree(agentKey string, requestedPath string, cursor string, limit int) (api.ProjectTreeResponse, error) {
	ws, err := s.resolveWorkspace(agentKey)
	if err != nil {
		return api.ProjectTreeResponse{}, err
	}
	relPath, candidate, err := resolveRelativeWorkspacePath(ws.roots, requestedPath, true)
	if err != nil {
		return api.ProjectTreeResponse{}, err
	}
	if relPath != "" {
		info, lstatErr := os.Lstat(filepath.Join(ws.roots.Workspace.Host, filepath.FromSlash(relPath)))
		if lstatErr != nil {
			return api.ProjectTreeResponse{}, mapFilesystemError(lstatErr, "directory not found")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return api.ProjectTreeResponse{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "directory symlinks cannot be expanded"}
		}
	}
	info, err := os.Stat(candidate.Host)
	if err != nil {
		return api.ProjectTreeResponse{}, mapFilesystemError(err, "directory not found")
	}
	if !info.IsDir() {
		return api.ProjectTreeResponse{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "path is not a directory"}
	}

	entries, err := s.readDirectory(ws.roots, relPath, candidate.Host)
	if err != nil {
		return api.ProjectTreeResponse{}, err
	}
	revision := treeRevision(entries)
	start, err := decodeCursor(cursor, revision)
	if err != nil {
		return api.ProjectTreeResponse{}, err
	}
	limit = normalizeLimit(limit)
	end := start + limit
	if end > len(entries) {
		end = len(entries)
	}
	if start > len(entries) {
		return api.ProjectTreeResponse{}, Error{Status: http.StatusConflict, Code: "directory_changed", Message: "directory changed while paging"}
	}
	page := make([]api.ProjectTreeEntry, end-start)
	copy(page, entries[start:end])
	nextCursor := ""
	if end < len(entries) {
		nextCursor = encodeCursor(pageCursor{Revision: revision, Offset: end})
	}
	return api.ProjectTreeResponse{
		AgentKey:      ws.definition.Key,
		Mode:          catalog.AgentModeForAPI(ws.definition.Mode),
		WorkspaceName: workspaceName(ws.roots.Workspace.Host),
		Path:          relPath,
		Revision:      revision,
		Entries:       page,
		NextCursor:    nextCursor,
	}, nil
}

func (s Service) Changes(agentKey string, chatID string, runID string, cursor string, limit int) (api.ProjectChangesResponse, error) {
	ws, err := s.resolveWorkspace(agentKey)
	if err != nil {
		return api.ProjectChangesResponse{}, err
	}
	if err := s.validateChat(ws.definition.Key, chatID, runID); err != nil {
		return api.ProjectChangesResponse{}, err
	}
	if s.History == nil {
		return api.ProjectChangesResponse{}, Error{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "file history is not configured"}
	}
	records, err := s.History.ListFileHistory(chatID, runID)
	if err != nil {
		return api.ProjectChangesResponse{}, mapHistoryError(err)
	}
	items := make([]api.ProjectChangeItem, 0, len(records))
	for _, record := range records {
		item, ok := projectChangeItem(ws.roots, record)
		if ok {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].UpdatedAt != items[j].UpdatedAt {
			return items[i].UpdatedAt > items[j].UpdatedAt
		}
		if items[i].RunID != items[j].RunID {
			return items[i].RunID > items[j].RunID
		}
		return items[i].Path < items[j].Path
	})
	revision := changesRevision(items)
	start, err := decodeCursor(cursor, revision)
	if err != nil {
		return api.ProjectChangesResponse{}, err
	}
	limit = normalizeLimit(limit)
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	if start > len(items) {
		return api.ProjectChangesResponse{}, Error{Status: http.StatusConflict, Code: "directory_changed", Message: "file changes changed while paging"}
	}
	runs := summarizeRuns(items)
	page := make([]api.ProjectChangeItem, end-start)
	copy(page, items[start:end])
	nextCursor := ""
	if end < len(items) {
		nextCursor = encodeCursor(pageCursor{Revision: revision, Offset: end})
	}
	return api.ProjectChangesResponse{
		AgentKey:   ws.definition.Key,
		ChatID:     chatID,
		Revision:   revision,
		Runs:       runs,
		Items:      page,
		NextCursor: nextCursor,
	}, nil
}

func (s Service) Diff(agentKey string, chatID string, runID string, requestedPath string, encoding string) (api.ProjectDiffResponse, error) {
	ws, err := s.resolveWorkspace(agentKey)
	if err != nil {
		return api.ProjectDiffResponse{}, err
	}
	if strings.TrimSpace(runID) == "" {
		return api.ProjectDiffResponse{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "runId is required"}
	}
	if err := s.validateChat(ws.definition.Key, chatID, runID); err != nil {
		return api.ProjectDiffResponse{}, err
	}
	if s.History == nil {
		return api.ProjectDiffResponse{}, Error{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "file history is not configured"}
	}
	relPath, target, err := resolveRelativeWorkspacePath(ws.roots, requestedPath, false)
	if err != nil {
		return api.ProjectDiffResponse{}, err
	}
	if filetools.IsBinaryExtension(target.Host) {
		return api.ProjectDiffResponse{}, Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "binary files do not support diff"}
	}
	records, err := s.History.ListFileHistory(chatID, runID)
	if err != nil {
		return api.ProjectDiffResponse{}, mapHistoryError(err)
	}
	var selected *contracts.FileHistoryRecord
	for index := range records {
		candidate, canonicalErr := pathutil.Canonicalize(records[index].FilePath)
		if canonicalErr == nil && candidate.Key == target.Key {
			selected = &records[index]
			break
		}
	}
	if selected == nil {
		return api.ProjectDiffResponse{}, Error{Status: http.StatusNotFound, Code: "not_found", Message: "file diff not found"}
	}
	maxBytes := s.MaxReadBytes
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	if (selected.Original.Exists && selected.Original.SizeBytes > int64(maxBytes)) ||
		(selected.Current.Exists && selected.Current.SizeBytes > int64(maxBytes)) {
		return api.ProjectDiffResponse{}, Error{Status: http.StatusRequestEntityTooLarge, Code: "diff_too_large", Message: "file diff exceeds the configured read limit"}
	}
	original, err := s.readDiffVersion(chatID, runID, selected.FilePath, "original", selected.Original, encoding)
	if err != nil {
		return api.ProjectDiffResponse{}, err
	}
	current, err := s.readDiffVersion(chatID, runID, selected.FilePath, "current", selected.Current, encoding)
	if err != nil {
		return api.ProjectDiffResponse{}, err
	}
	return api.ProjectDiffResponse{
		AgentKey:   ws.definition.Key,
		ChatID:     chatID,
		RunID:      runID,
		Path:       relPath,
		ChangeType: changeType(selected.Original.Exists, selected.Current.Exists),
		Original:   original,
		Current:    current,
	}, nil
}

func (s Service) resolveWorkspace(agentKey string) (workspace, error) {
	if s.Registry == nil {
		return workspace{}, Error{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "agent registry is not configured"}
	}
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" {
		return workspace{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "agentKey is required"}
	}
	def, ok := s.Registry.AgentDefinition(agentKey)
	if !ok {
		return workspace{}, Error{Status: http.StatusNotFound, Code: "not_found", Message: "agent not found"}
	}
	mode := strings.ToUpper(strings.TrimSpace(def.Mode))
	if mode != catalog.AgentModeCoder && mode != catalog.AgentModeKBase {
		return workspace{}, Error{Status: http.StatusBadRequest, Code: "project_not_supported", Message: "project browsing only supports CODER or KBASE agents"}
	}
	root := strings.TrimSpace(def.Workspace.Root)
	if root == "" {
		return workspace{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "agent workspace is not a stable directory"}
	}
	root = filepath.Clean(pathutil.ExpandHome(root))
	if !filepath.IsAbs(root) {
		return workspace{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "agent workspace must be an absolute path"}
	}
	info, err := os.Stat(root)
	if err != nil {
		return workspace{}, mapFilesystemError(err, "workspace directory not found")
	}
	if !info.IsDir() {
		return workspace{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "agent workspace is not a directory"}
	}
	roots, err := rootpaths.New(root, s.ChatsRoot, "")
	if err != nil {
		return workspace{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: err.Error()}
	}
	return workspace{definition: def, roots: roots}, nil
}

func (s Service) validateChat(agentKey string, chatID string, runID string) error {
	if s.Chats == nil {
		return Error{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "chat store is not configured"}
	}
	chatID = strings.TrimSpace(chatID)
	if !chat.ValidChatID(chatID) {
		return Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "chatId is required"}
	}
	summary, err := s.Chats.Summary(chatID)
	if err != nil || summary == nil {
		if errors.Is(err, chat.ErrChatNotFound) || errors.Is(err, os.ErrNotExist) || summary == nil {
			return Error{Status: http.StatusNotFound, Code: "not_found", Message: "chat not found"}
		}
		return Error{Status: http.StatusInternalServerError, Code: "internal_error", Message: err.Error()}
	}
	if strings.TrimSpace(summary.TeamID) != "" || strings.TrimSpace(summary.AgentKey) != strings.TrimSpace(agentKey) {
		return Error{Status: http.StatusForbidden, Code: "forbidden", Message: "chat does not belong to the requested agent"}
	}
	runID = strings.TrimSpace(runID)
	if runID != "" {
		query, queryErr := s.Chats.LoadRunQuery(chatID, runID)
		if queryErr != nil || query == nil {
			return Error{Status: http.StatusNotFound, Code: "not_found", Message: "run not found"}
		}
	}
	return nil
}

func (s Service) readDirectory(roots rootpaths.Roots, relPath string, directory string) ([]api.ProjectTreeEntry, error) {
	items, err := os.ReadDir(directory)
	if err != nil {
		return nil, mapFilesystemError(err, "directory not found")
	}
	entries := make([]api.ProjectTreeEntry, 0, len(items))
	for _, item := range items {
		info, infoErr := item.Info()
		if infoErr != nil {
			continue
		}
		childRel := item.Name()
		if relPath != "" {
			childRel = pathpkg.Join(relPath, item.Name())
		}
		childHost := filepath.Join(roots.Workspace.Host, filepath.FromSlash(childRel))
		entry := api.ProjectTreeEntry{
			Name:           item.Name(),
			Path:           filepath.ToSlash(filepath.Clean(childRel)),
			Accessible:     true,
			ModifiedUnixMs: info.ModTime().UnixMilli(),
		}
		if info.Mode()&os.ModeSymlink != 0 {
			entry.Kind = "symlink"
			canonical, canonicalErr := pathutil.Canonicalize(childHost)
			if canonicalErr != nil || roots.ClassifyCanonical(canonical) != rootpaths.ZoneWorkspace || filetools.IsBlockedDeviceFile(canonical.Host) {
				entry.Accessible = false
				entries = append(entries, entry)
				continue
			}
			targetInfo, targetErr := os.Stat(childHost)
			if targetErr != nil {
				entry.Accessible = false
			} else if targetInfo.IsDir() {
				entry.TargetKind = "directory"
				entry.Accessible = false
			} else if targetInfo.Mode().IsRegular() {
				entry.TargetKind = "file"
				entry.SizeBytes = targetInfo.Size()
			} else {
				entry.Accessible = false
			}
			entries = append(entries, entry)
			continue
		}
		canonical, canonicalErr := pathutil.Canonicalize(childHost)
		if canonicalErr != nil {
			continue
		}
		zone := roots.ClassifyCanonical(canonical)
		if zone == rootpaths.ZoneCurrentChat || zone == rootpaths.ZoneOtherChat {
			continue
		}
		if zone != rootpaths.ZoneWorkspace || filetools.IsBlockedDeviceFile(canonical.Host) {
			continue
		}
		switch {
		case info.IsDir():
			entry.Kind = "directory"
		case info.Mode().IsRegular():
			entry.Kind = "file"
			entry.SizeBytes = info.Size()
		default:
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		ri, rj := treeKindRank(entries[i].Kind), treeKindRank(entries[j].Kind)
		if ri != rj {
			return ri < rj
		}
		li, lj := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if li != lj {
			return li < lj
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

func (s Service) readDiffVersion(chatID string, runID string, filePath string, version string, metadata contracts.FileHistoryVersion, requestedEncoding string) (api.ProjectDiffVersion, error) {
	result := api.ProjectDiffVersion{
		Exists:    metadata.Exists,
		SHA256:    metadata.SHA256,
		SizeBytes: metadata.SizeBytes,
	}
	if !metadata.Exists {
		return result, nil
	}
	content, err := s.History.ReadFileHistory(chatID, runID, filePath, version)
	if err != nil {
		return api.ProjectDiffVersion{}, mapHistoryError(err)
	}
	decoded, ok, decodeErr := textcodec.DecodeFileText([]byte(content), requestedEncoding, runtimeenv.Detect())
	if decodeErr != nil || !ok || !textcodec.LooksLikeDecodedText(decoded.Content) {
		message := "file history is not decodable text"
		if decodeErr != nil {
			message = decodeErr.Error()
		}
		return api.ProjectDiffVersion{}, Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: message}
	}
	result.Content = decoded.Content
	result.Encoding = decoded.Encoding
	return result, nil
}

func resolveRelativeWorkspacePath(roots rootpaths.Roots, raw string, allowEmpty bool) (string, pathutil.Canonical, error) {
	raw = strings.TrimSpace(raw)
	if strings.ContainsRune(raw, 0) || strings.Contains(raw, `\`) {
		return "", pathutil.Canonical{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "path must be a POSIX workspace-relative path"}
	}
	if raw == "" {
		if !allowEmpty {
			return "", pathutil.Canonical{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "path is required"}
		}
		return "", roots.Workspace, nil
	}
	if pathpkg.IsAbs(raw) {
		return "", pathutil.Canonical{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "path must be workspace-relative"}
	}
	clean := pathpkg.Clean(raw)
	if clean == "." {
		if allowEmpty {
			return "", roots.Workspace, nil
		}
		return "", pathutil.Canonical{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "path is required"}
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", pathutil.Canonical{}, Error{Status: http.StatusForbidden, Code: "forbidden", Message: "path is outside agent workspace"}
	}
	candidate, err := pathutil.Canonicalize(filepath.Join(roots.Workspace.Host, filepath.FromSlash(clean)))
	if err != nil {
		return "", pathutil.Canonical{}, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: err.Error()}
	}
	switch roots.ClassifyCanonical(candidate) {
	case rootpaths.ZoneCurrentChat, rootpaths.ZoneOtherChat:
		return "", pathutil.Canonical{}, Error{Status: http.StatusForbidden, Code: "path_crosses_chat_root", Message: "workspace path must not enter the chats root"}
	case rootpaths.ZoneWorkspace:
		return clean, candidate, nil
	default:
		return "", pathutil.Canonical{}, Error{Status: http.StatusForbidden, Code: "forbidden", Message: "path is outside agent workspace"}
	}
}

func projectChangeItem(roots rootpaths.Roots, record contracts.FileHistoryRecord) (api.ProjectChangeItem, bool) {
	if !record.Original.Present || !record.Current.Present {
		return api.ProjectChangeItem{}, false
	}
	candidate, err := pathutil.Canonicalize(record.FilePath)
	if err != nil || roots.ClassifyCanonical(candidate) != rootpaths.ZoneWorkspace {
		return api.ProjectChangeItem{}, false
	}
	rel, err := filepath.Rel(roots.Workspace.Host, candidate.Host)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(filepath.ToSlash(rel), "../") {
		return api.ProjectChangeItem{}, false
	}
	return api.ProjectChangeItem{
		RunID:      record.RunID,
		Path:       filepath.ToSlash(filepath.Clean(rel)),
		ChangeType: changeType(record.Original.Exists, record.Current.Exists),
		UpdatedAt:  record.UpdatedAtUnixMs,
		Original:   api.ProjectHistoryVersion{Exists: record.Original.Exists, SHA256: record.Original.SHA256, SizeBytes: record.Original.SizeBytes},
		Current:    api.ProjectHistoryVersion{Exists: record.Current.Exists, SHA256: record.Current.SHA256, SizeBytes: record.Current.SizeBytes},
	}, true
}

func summarizeRuns(items []api.ProjectChangeItem) []api.ProjectChangeRun {
	byRun := map[string]api.ProjectChangeRun{}
	for _, item := range items {
		run := byRun[item.RunID]
		run.RunID = item.RunID
		run.FileCount++
		if item.UpdatedAt > run.UpdatedAt {
			run.UpdatedAt = item.UpdatedAt
		}
		byRun[item.RunID] = run
	}
	runs := make([]api.ProjectChangeRun, 0, len(byRun))
	for _, run := range byRun {
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].UpdatedAt != runs[j].UpdatedAt {
			return runs[i].UpdatedAt > runs[j].UpdatedAt
		}
		return runs[i].RunID > runs[j].RunID
	})
	return runs
}

func changeType(originalExists bool, currentExists bool) string {
	switch {
	case !originalExists && currentExists:
		return "added"
	case originalExists && !currentExists:
		return "deleted"
	default:
		return "modified"
	}
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultPageLimit
	}
	if limit > MaxPageLimit {
		return MaxPageLimit
	}
	return limit
}

func encodeCursor(cursor pageCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeCursor(raw string, revision string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "invalid cursor"}
	}
	var cursor pageCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Offset < 0 {
		return 0, Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "invalid cursor"}
	}
	if cursor.Revision != revision {
		return 0, Error{Status: http.StatusConflict, Code: "directory_changed", Message: "collection changed while paging"}
	}
	return cursor.Offset, nil
}

func treeRevision(entries []api.ProjectTreeEntry) string {
	hash := sha256.New()
	for _, entry := range entries {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s\x00%t\x00%d\x00%d\n", entry.Name, entry.Path, entry.Kind, entry.TargetKind, entry.Accessible, entry.SizeBytes, entry.ModifiedUnixMs)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func changesRevision(items []api.ProjectChangeItem) string {
	hash := sha256.New()
	for _, item := range items {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%d\x00%s\x00%s\n", item.RunID, item.Path, item.ChangeType, item.UpdatedAt, item.Original.SHA256, item.Current.SHA256)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func treeKindRank(kind string) int {
	switch kind {
	case "directory":
		return 0
	case "file":
		return 1
	default:
		return 2
	}
}

func workspaceName(root string) string {
	name := filepath.Base(filepath.Clean(root))
	if name == "." || name == string(filepath.Separator) {
		return root
	}
	return name
}

func mapFilesystemError(err error, notFoundMessage string) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Error{Status: http.StatusNotFound, Code: "not_found", Message: notFoundMessage}
	case errors.Is(err, os.ErrPermission):
		return Error{Status: http.StatusForbidden, Code: "forbidden", Message: "workspace path is not readable"}
	default:
		return Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: err.Error()}
	}
}

func mapHistoryError(err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Error{Status: http.StatusNotFound, Code: "not_found", Message: "file history not found"}
	case errors.Is(err, os.ErrPermission):
		return Error{Status: http.StatusBadRequest, Code: "invalid_request", Message: "invalid file history request"}
	default:
		return Error{Status: http.StatusInternalServerError, Code: "internal_error", Message: err.Error()}
	}
}
