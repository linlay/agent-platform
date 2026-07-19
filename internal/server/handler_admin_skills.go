package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

type adminSkillRegistry interface {
	AdminSkills() ([]catalog.AdminSkill, error)
	AdminSkill(key string) (catalog.AdminSkill, bool, error)
	CreateEditableSkill(key string, skillMd string, files []catalog.EditableSkillInlineFile) (catalog.AdminSkill, error)
	DeleteEditableSkill(key string) error
	EditableSkillUsage(key string) ([]string, error)
	ReadEditableSkillFile(key string, relPath string) (catalog.EditableSkillFileContent, error)
	ResolveEditableSkillFile(key string, relPath string) (string, catalog.EditableSkillFile, error)
	WriteEditableSkillFile(key string, relPath string, content string, encoding string, baseSHA256 string) (catalog.EditableSkillFile, error)
	DeleteEditableSkillFile(key string, relPath string, recursive bool, baseSHA256 string) error
	MkdirEditableSkillFile(key string, relPath string) (catalog.EditableSkillFile, error)
	RenameEditableSkillFile(key string, fromPath string, toPath string, overwrite bool) (catalog.EditableSkillFile, error)
	UploadEditableSkillFile(key string, relPath string, src io.Reader, overwrite bool) (catalog.EditableSkillFile, error)
}

func (s *Server) adminSkillRegistry() (adminSkillRegistry, error) {
	registry, ok := s.deps.Registry.(adminSkillRegistry)
	if !ok || registry == nil {
		return nil, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "skill admin registry is not configured")
	}
	return registry, nil
}

func (s *Server) listAdminSkills() ([]api.AdminSkillSummary, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return nil, err
	}
	items, err := registry.AdminSkills()
	if err != nil {
		return nil, err
	}
	response := make([]api.AdminSkillSummary, 0, len(items))
	for _, item := range items {
		response = append(response, buildAdminSkillSummary(item))
	}
	return response, nil
}

func (s *Server) handleAdminSkillDetail(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if strings.TrimSpace(key) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "key is required"))
		return
	}
	response, err := s.adminSkillDetail(key, r.URL.Query().Get("openPath"))
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateAdminSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	if strings.TrimSpace(req.SkillMd) == "" {
		req.SkillMd = "---\nname: " + strings.TrimSpace(req.Key) + "\ndescription: \n---\n\n"
	}
	created, err := s.createAdminSkill(r.Context(), req)
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	response, err := s.adminSkillDetail(created.Key, "SKILL.md")
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteAdminSkillRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	if req.Key == "" {
		req.Key = r.URL.Query().Get("key")
	}
	response, err := s.deleteAdminSkill(r.Context(), req.Key)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillFile(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		response, err := s.readAdminSkillFile(r.URL.Query().Get("key"), r.URL.Query().Get("path"))
		s.writeAgentHTTPResponse(w, response, err)
	case http.MethodPut:
		var req api.WriteAdminSkillFileRequest
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
			return
		}
		response, err := s.writeAdminSkillFile(r.Context(), req)
		s.writeAgentHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func (s *Server) handleAdminSkillFileCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateAdminSkillFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.createAdminSkillFile(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillFileMkdir(w http.ResponseWriter, r *http.Request) {
	var req api.MkdirAdminSkillFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.mkdirAdminSkillFile(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillFileRename(w http.ResponseWriter, r *http.Request) {
	var req api.RenameAdminSkillFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.renameAdminSkillFile(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillFileDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteAdminSkillFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.deleteAdminSkillFile(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillFileUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, catalog.EditableSkillMaxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(catalog.EditableSkillMaxUploadBytes); err != nil {
		s.writeAgentHTTPResponse(w, nil, mapSkillEditError(catalog.ErrSkillFileTooLarge))
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	relPath := strings.TrimSpace(r.FormValue("path"))
	overwrite := parseLooseBool(r.FormValue("overwrite"))
	if key == "" || relPath == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "key and path are required"))
		return
	}
	file, _, err := pickUploadFile(r.MultipartForm)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	defer file.Close()
	response, err := s.uploadAdminSkillFile(r.Context(), key, relPath, file, overwrite)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillValidate(w http.ResponseWriter, r *http.Request) {
	var req api.ValidateAdminSkillRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	if req.Key == "" {
		req.Key = r.URL.Query().Get("key")
	}
	response, err := s.validateAdminSkill(r.Context(), req.Key)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) createAdminSkill(ctx context.Context, req api.CreateAdminSkillRequest) (catalog.AdminSkill, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return catalog.AdminSkill{}, err
	}
	files := make([]catalog.EditableSkillInlineFile, 0, len(req.Files))
	for _, file := range req.Files {
		files = append(files, catalog.EditableSkillInlineFile{Path: file.Path, Content: file.Content, Encoding: file.Encoding})
	}
	item, err := registry.CreateEditableSkill(req.Key, req.SkillMd, files)
	if err != nil {
		return catalog.AdminSkill{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return catalog.AdminSkill{}, err
	}
	if refreshed, found, err := registry.AdminSkill(item.Key); err == nil && found {
		item = refreshed
	}
	return item, nil
}

func (s *Server) deleteAdminSkill(ctx context.Context, key string) (api.DeleteAdminSkillResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.DeleteAdminSkillResponse{}, err
	}
	usage, err := registry.EditableSkillUsage(key)
	if err != nil {
		return api.DeleteAdminSkillResponse{}, mapSkillEditError(err)
	}
	if len(usage) > 0 {
		return api.DeleteAdminSkillResponse{}, newAgentStatusErrorWithData(http.StatusConflict, "conflict", "skill is used by agents", map[string]any{"usedByAgents": usage})
	}
	if err := registry.DeleteEditableSkill(key); err != nil {
		return api.DeleteAdminSkillResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.DeleteAdminSkillResponse{}, err
	}
	return api.DeleteAdminSkillResponse{Key: strings.TrimSpace(key), Deleted: true}, nil
}

func (s *Server) handleAdminSkillFileDownload(w http.ResponseWriter, r *http.Request) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	pathOnDisk, metadata, err := registry.ResolveEditableSkillFile(r.URL.Query().Get("key"), r.URL.Query().Get("path"))
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, mapSkillEditError(err))
		return
	}
	file, err := os.Open(pathOnDisk)
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, mapSkillEditError(err))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		s.writeAgentHTTPResponse(w, nil, mapSkillEditError(catalog.ErrSkillIsDirectory))
		return
	}
	if metadata.MimeType != "" {
		w.Header().Set("Content-Type", metadata.MimeType)
	}
	http.ServeContent(w, r, metadata.Name, info.ModTime(), file)
}

func (s *Server) adminSkillDetail(key string, openPath string) (api.AdminSkillDetailResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillDetailResponse{}, err
	}
	item, found, err := registry.AdminSkill(key)
	if err != nil {
		return api.AdminSkillDetailResponse{}, mapSkillEditError(err)
	}
	if !found {
		return api.AdminSkillDetailResponse{}, newAgentStatusError(http.StatusNotFound, "not_found", "skill not found")
	}
	response := buildAdminSkillDetail(item)
	openPath = strings.TrimSpace(openPath)
	if openPath == "" {
		return response, nil
	}
	entry := findAdminSkillEntry(response.FileManifest.Entries, openPath)
	if entry == nil || entry.ContentKind != "text" || !entry.Editable {
		return response, nil
	}
	file, err := registry.ReadEditableSkillFile(item.Key, openPath)
	if err != nil {
		return api.AdminSkillDetailResponse{}, mapSkillEditError(err)
	}
	response.OpenedFile = apiAdminSkillTextFile(file, true)
	return response, nil
}

func (s *Server) readAdminSkillFile(key string, relPath string) (api.AdminSkillTextFile, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillTextFile{}, err
	}
	file, err := registry.ReadEditableSkillFile(key, relPath)
	if err != nil {
		return api.AdminSkillTextFile{}, mapSkillEditError(err)
	}
	text := apiAdminSkillTextFile(file, true)
	if text == nil {
		return api.AdminSkillTextFile{}, newAgentStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", "file is not editable text")
	}
	return *text, nil
}

func (s *Server) writeAdminSkillFile(ctx context.Context, req api.WriteAdminSkillFileRequest) (api.AdminSkillMutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	file, err := registry.WriteEditableSkillFile(req.Key, req.Path, req.Content, req.Encoding, req.BaseSHA256)
	if err != nil {
		return api.AdminSkillMutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	item, err := adminSkillItem(registry, req.Key)
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	opened := apiAdminSkillTextFile(catalog.EditableSkillFileContent{
		Key:       strings.TrimSpace(req.Key),
		Path:      file.Path,
		Content:   req.Content,
		Encoding:  firstNonBlank(req.Encoding, "utf-8"),
		SHA256:    file.SHA256,
		Size:      file.Size,
		UpdatedAt: file.UpdatedAt,
	}, true)
	return buildAdminSkillMutation(item, "save", file.Path, opened, false), nil
}

func (s *Server) createAdminSkillFile(ctx context.Context, req api.CreateAdminSkillFileRequest) (api.AdminSkillMutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	if _, _, err := registry.ResolveEditableSkillFile(req.Key, req.Path); err == nil {
		return api.AdminSkillMutationResponse{}, mapSkillEditError(catalog.ErrSkillConflict)
	} else if !errors.Is(err, catalog.ErrSkillNotFound) {
		return api.AdminSkillMutationResponse{}, mapSkillEditError(err)
	}
	file, err := registry.WriteEditableSkillFile(req.Key, req.Path, req.Content, req.Encoding, "")
	if err != nil {
		return api.AdminSkillMutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	item, err := adminSkillItem(registry, req.Key)
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	opened := apiAdminSkillTextFile(catalog.EditableSkillFileContent{
		Key:       strings.TrimSpace(req.Key),
		Path:      file.Path,
		Content:   req.Content,
		Encoding:  firstNonBlank(req.Encoding, "utf-8"),
		SHA256:    file.SHA256,
		Size:      file.Size,
		UpdatedAt: file.UpdatedAt,
	}, true)
	return buildAdminSkillMutation(item, "create", file.Path, opened, true), nil
}

func (s *Server) mkdirAdminSkillFile(ctx context.Context, req api.MkdirAdminSkillFileRequest) (api.AdminSkillMutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	file, err := registry.MkdirEditableSkillFile(req.Key, req.Path)
	if err != nil {
		return api.AdminSkillMutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	item, err := adminSkillItem(registry, req.Key)
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	return buildAdminSkillMutation(item, "mkdir", file.Path, nil, true), nil
}

func (s *Server) renameAdminSkillFile(ctx context.Context, req api.RenameAdminSkillFileRequest) (api.AdminSkillMutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	file, err := registry.RenameEditableSkillFile(req.Key, req.FromPath, req.ToPath, req.Overwrite)
	if err != nil {
		return api.AdminSkillMutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	item, err := adminSkillItem(registry, req.Key)
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	return buildAdminSkillMutation(item, "rename", file.Path, nil, true), nil
}

func (s *Server) deleteAdminSkillFile(ctx context.Context, req api.DeleteAdminSkillFileRequest) (api.AdminSkillMutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	if err := registry.DeleteEditableSkillFile(req.Key, req.Path, req.Recursive, req.BaseSHA256); err != nil {
		return api.AdminSkillMutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	item, err := adminSkillItem(registry, req.Key)
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	manifest := buildAdminSkillFileManifest(item.Files)
	return buildAdminSkillMutation(item, "delete", manifest.DefaultOpenPath, nil, true), nil
}

func (s *Server) uploadAdminSkillFile(ctx context.Context, key string, relPath string, src io.Reader, overwrite bool) (api.AdminSkillMutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	file, err := registry.UploadEditableSkillFile(key, relPath, src, overwrite)
	if err != nil {
		return api.AdminSkillMutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	item, err := adminSkillItem(registry, key)
	if err != nil {
		return api.AdminSkillMutationResponse{}, err
	}
	return buildAdminSkillMutation(item, "upload", file.Path, nil, true), nil
}

func (s *Server) validateAdminSkill(ctx context.Context, key string) (api.AdminSkillValidateResponse, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return api.AdminSkillValidateResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "key is required")
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillValidateResponse{}, err
	}
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillValidateResponse{}, err
	}
	item, found, err := registry.AdminSkill(key)
	if err != nil {
		return api.AdminSkillValidateResponse{}, mapSkillEditError(err)
	}
	if !found {
		return api.AdminSkillValidateResponse{}, newAgentStatusError(http.StatusNotFound, "not_found", "skill not found")
	}
	return api.AdminSkillValidateResponse{
		Key:         item.Key,
		Status:      firstNonBlank(item.Status, catalog.AdminSkillStatusInvalid),
		Diagnostics: adminSkillDiagnostics(item.Diagnostics),
		UpdatedAt:   item.UpdatedAt,
		Size:        item.Size,
	}, nil
}

func (s *Server) reloadAdminSkills(ctx context.Context) error {
	if s.deps.CatalogReloader != nil {
		return s.deps.CatalogReloader.Reload(ctx, "skills")
	}
	if s.deps.Registry != nil {
		if err := s.deps.Registry.Reload(ctx, "skills"); err != nil {
			return err
		}
		if err := s.reloadAgentCatalog(ctx); err != nil {
			return err
		}
	}
	s.broadcast("catalog.updated", catalogUpdatedPushPayload("skills", time.Now().UnixMilli()))
	return nil
}

func buildAdminSkillDetail(item catalog.AdminSkill) api.AdminSkillDetailResponse {
	return api.AdminSkillDetailResponse{
		Skill:        buildAdminSkillSummary(item),
		Capabilities: buildAdminSkillCapabilities(),
		FileManifest: buildAdminSkillFileManifest(item.Files),
		Diagnostics:  adminSkillDiagnostics(item.Diagnostics),
	}
}

func buildAdminSkillSummary(item catalog.AdminSkill) api.AdminSkillSummary {
	summary := api.AdminSkillSummary{
		Key:          item.Key,
		Name:         firstNonBlank(item.Name, item.Key),
		Description:  item.Description,
		Meta:         cloneMeta(item.Meta),
		Status:       firstNonBlank(item.Status, catalog.AdminSkillStatusInvalid),
		UpdatedAt:    item.UpdatedAt,
		Size:         item.Size,
		UsedByAgents: append([]string(nil), item.UsedByAgents...),
		Source: &api.AgentSource{
			Kind:     item.Source.Kind,
			Path:     item.Source.Path,
			AgentDir: item.Source.SkillDir,
		},
	}
	if item.IconPath != "" {
		summary.Icon = adminSkillIconURL(item.Key, item.IconPath)
	}
	if len(item.Diagnostics) > 0 {
		first := item.Diagnostics[0]
		summary.Diagnostic = &api.AdminRegistryListDiagnostic{
			Severity: first.Severity,
			Code:     first.Code,
			Message:  first.Message,
		}
		summary.DiagnosticCount = len(item.Diagnostics)
	}
	return summary
}

func adminSkillIconURL(key string, relPath string) string {
	values := url.Values{}
	values.Set("key", key)
	values.Set("path", relPath)
	return "/api/admin/skills/file/download?" + values.Encode()
}

func buildAdminSkillCapabilities() api.AdminSkillCapabilities {
	return api.AdminSkillCapabilities{
		MaxTextBytes:   catalog.EditableSkillMaxTextBytes,
		MaxUploadBytes: catalog.EditableSkillMaxUploadBytes,
		CanCreate:      true,
		CanRename:      true,
		CanDelete:      true,
		CanUpload:      true,
		CanDownload:    true,
	}
}

func buildAdminSkillFileManifest(files []catalog.EditableSkillFile) api.AdminSkillFileManifest {
	entryByPath := map[string]api.AdminSkillFileEntry{}
	for _, file := range files {
		entry := adminSkillEntryFromFile(file)
		entryByPath[entry.Path] = entry
		for parent := entry.ParentPath; parent != ""; parent = adminSkillParentPath(parent) {
			if _, exists := entryByPath[parent]; exists {
				continue
			}
			entryByPath[parent] = api.AdminSkillFileEntry{
				Path:         parent,
				Name:         path.Base(parent),
				Kind:         "directory",
				ParentPath:   adminSkillParentPath(parent),
				Depth:        adminSkillPathDepth(parent),
				ContentKind:  "directory",
				Renamable:    true,
				Deletable:    true,
				Downloadable: false,
				Uploadable:   false,
				Editable:     false,
			}
		}
	}

	children := map[string][]string{}
	for p, entry := range entryByPath {
		children[entry.ParentPath] = append(children[entry.ParentPath], p)
	}
	for parent := range children {
		sort.SliceStable(children[parent], func(i, j int) bool {
			return adminSkillEntryLess(entryByPath[children[parent][i]], entryByPath[children[parent][j]], parent == "")
		})
	}

	entries := make([]api.AdminSkillFileEntry, 0, len(entryByPath))
	var appendChildren func(parent string)
	appendChildren = func(parent string) {
		for _, childPath := range children[parent] {
			entry := entryByPath[childPath]
			entry.Order = len(entries)
			entries = append(entries, entry)
			if entry.Kind == "directory" {
				appendChildren(entry.Path)
			}
		}
	}
	appendChildren("")

	var counts api.AdminSkillFileCounts
	var defaultOpenPath string
	for _, entry := range entries {
		switch entry.Kind {
		case "directory":
			counts.Directories++
		default:
			counts.Files++
			counts.TotalSize += entry.Size
			if entry.ContentKind == "text" {
				counts.TextFiles++
				if defaultOpenPath == "" || entry.Role == "skillMd" {
					defaultOpenPath = entry.Path
				}
			} else if entry.ContentKind == "binary" {
				counts.BinaryFiles++
			}
		}
	}

	return api.AdminSkillFileManifest{
		Revision:        adminSkillManifestRevision(entries),
		DefaultOpenPath: defaultOpenPath,
		Counts:          counts,
		Entries:         entries,
	}
}

func adminSkillEntryFromFile(file catalog.EditableSkillFile) api.AdminSkillFileEntry {
	contentKind := "binary"
	if file.Kind == "directory" {
		contentKind = "directory"
	} else if file.Text {
		contentKind = "text"
	}
	role := adminSkillFileRole(file.Path)
	editable := file.Kind == "file" && contentKind == "text"
	return api.AdminSkillFileEntry{
		Path:         file.Path,
		Name:         file.Name,
		Kind:         file.Kind,
		ParentPath:   adminSkillParentPath(file.Path),
		Depth:        adminSkillPathDepth(file.Path),
		Size:         file.Size,
		UpdatedAt:    file.UpdatedAt,
		MimeType:     file.MimeType,
		SHA256:       file.SHA256,
		ContentKind:  contentKind,
		Language:     adminSkillFileLanguage(file.Path, contentKind),
		Role:         role,
		Editable:     editable,
		Downloadable: file.Kind == "file",
		Uploadable:   file.Kind == "file",
		Renamable:    file.Path != "SKILL.md",
		Deletable:    file.Path != "SKILL.md",
	}
}

func adminSkillEntryLess(a api.AdminSkillFileEntry, b api.AdminSkillFileEntry, root bool) bool {
	if root {
		ap := adminSkillRootPriority(a)
		bp := adminSkillRootPriority(b)
		if ap != bp {
			return ap < bp
		}
	}
	if a.Kind != b.Kind {
		return a.Kind == "directory"
	}
	if !strings.EqualFold(a.Name, b.Name) {
		return naturalLess(a.Name, b.Name)
	}
	return a.Path < b.Path
}

func adminSkillRootPriority(entry api.AdminSkillFileEntry) int {
	switch entry.Role {
	case "skillMd":
		return 0
	case "readme":
		return 1
	}
	if entry.Kind == "directory" {
		return 2
	}
	return 3
}

func naturalLess(a string, b string) bool {
	aa := strings.ToLower(a)
	bb := strings.ToLower(b)
	for i, j := 0, 0; i < len(aa) && j < len(bb); {
		if isASCIIDigit(aa[i]) && isASCIIDigit(bb[j]) {
			istart, jstart := i, j
			for i < len(aa) && isASCIIDigit(aa[i]) {
				i++
			}
			for j < len(bb) && isASCIIDigit(bb[j]) {
				j++
			}
			ai, aerr := strconv.Atoi(strings.TrimLeft(aa[istart:i], "0"))
			bi, berr := strconv.Atoi(strings.TrimLeft(bb[jstart:j], "0"))
			if aerr == nil && berr == nil && ai != bi {
				return ai < bi
			}
			if spanA, spanB := i-istart, j-jstart; spanA != spanB {
				return spanA < spanB
			}
			continue
		}
		if aa[i] != bb[j] {
			return aa[i] < bb[j]
		}
		i++
		j++
	}
	return len(aa) < len(bb)
}

func isASCIIDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

func adminSkillParentPath(relPath string) string {
	parent := path.Dir(strings.TrimSpace(relPath))
	if parent == "." || parent == "/" {
		return ""
	}
	return parent
}

func adminSkillPathDepth(relPath string) int {
	relPath = strings.Trim(strings.TrimSpace(relPath), "/")
	if relPath == "" {
		return 0
	}
	return strings.Count(relPath, "/")
}

func adminSkillFileRole(relPath string) string {
	clean := path.Clean(strings.TrimSpace(relPath))
	switch {
	case clean == "SKILL.md":
		return "skillMd"
	case strings.EqualFold(clean, "README.md"):
		return "readme"
	case strings.HasPrefix(clean, "references/"):
		return "reference"
	case strings.HasPrefix(clean, "scripts/"):
		return "script"
	case strings.HasPrefix(clean, "assets/"):
		return "asset"
	default:
		return "other"
	}
}

func adminSkillFileLanguage(relPath string, contentKind string) string {
	if contentKind != "text" {
		return ""
	}
	ext := strings.ToLower(path.Ext(relPath))
	switch ext {
	case ".md", ".markdown":
		return "markdown"
	case ".py":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js":
		return "javascript"
	case ".jsx":
		return "jsx"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".sh", ".bash":
		return "shell"
	case ".toml":
		return "toml"
	case ".env":
		return "env"
	case ".css":
		return "css"
	default:
		return "plain"
	}
}

func adminSkillManifestRevision(entries []api.AdminSkillFileEntry) string {
	hash := sha256.New()
	for _, entry := range entries {
		_, _ = io.WriteString(hash, entry.Path)
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, entry.Kind)
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, entry.SHA256)
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, strconv.FormatInt(entry.UpdatedAt, 10))
		_, _ = io.WriteString(hash, "\x00")
		_, _ = io.WriteString(hash, strconv.FormatInt(entry.Size, 10))
		_, _ = io.WriteString(hash, "\x00")
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func findAdminSkillEntry(entries []api.AdminSkillFileEntry, relPath string) *api.AdminSkillFileEntry {
	relPath = strings.TrimSpace(relPath)
	for i := range entries {
		if entries[i].Path == relPath {
			return &entries[i]
		}
	}
	return nil
}

func apiAdminSkillTextFile(file catalog.EditableSkillFileContent, editable bool) *api.AdminSkillTextFile {
	encoding := firstNonBlank(file.Encoding, "utf-8")
	return &api.AdminSkillTextFile{
		Key:       file.Key,
		Path:      file.Path,
		Content:   file.Content,
		Encoding:  encoding,
		SHA256:    file.SHA256,
		Size:      file.Size,
		UpdatedAt: file.UpdatedAt,
		Editable:  editable,
	}
}

func adminSkillItem(registry adminSkillRegistry, key string) (catalog.AdminSkill, error) {
	item, found, err := registry.AdminSkill(key)
	if err != nil {
		return catalog.AdminSkill{}, mapSkillEditError(err)
	}
	if !found {
		return catalog.AdminSkill{}, newAgentStatusError(http.StatusNotFound, "not_found", "skill not found")
	}
	return item, nil
}

func buildAdminSkillMutation(item catalog.AdminSkill, action string, selectedPath string, opened *api.AdminSkillTextFile, includeManifest bool) api.AdminSkillMutationResponse {
	manifest := buildAdminSkillFileManifest(item.Files)
	if selectedPath == "" {
		selectedPath = manifest.DefaultOpenPath
	}
	entry := findAdminSkillEntry(manifest.Entries, selectedPath)
	skill := buildAdminSkillSummary(item)
	response := api.AdminSkillMutationResponse{
		Key:          item.Key,
		Action:       action,
		SelectedPath: selectedPath,
		OpenedFile:   opened,
		Skill:        &skill,
		Diagnostics:  adminSkillDiagnostics(item.Diagnostics),
		Reloaded:     true,
	}
	if entry != nil {
		entryCopy := *entry
		response.Entry = &entryCopy
	}
	if includeManifest {
		response.FileManifest = &manifest
	}
	return response
}

func adminSkillDiagnostics(items []catalog.AdminSkillDiagnostic) []api.AdminAgentDiagnostic {
	if len(items) == 0 {
		return nil
	}
	out := make([]api.AdminAgentDiagnostic, 0, len(items))
	for _, item := range items {
		out = append(out, api.AdminAgentDiagnostic{
			Severity:   item.Severity,
			Code:       item.Code,
			Message:    item.Message,
			SourcePath: item.SourcePath,
		})
	}
	return out
}

func mapSkillEditError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, catalog.ErrSkillNotFound):
		return newAgentStatusError(http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, catalog.ErrSkillAlreadyExists), errors.Is(err, catalog.ErrSkillConflict):
		return newAgentStatusError(http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, catalog.ErrSkillFileTooLarge):
		return newAgentStatusError(http.StatusRequestEntityTooLarge, "payload_too_large", err.Error())
	case errors.Is(err, catalog.ErrSkillFileBinary), errors.Is(err, catalog.ErrSkillUnsupportedEncoding):
		return newAgentStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", err.Error())
	case errors.Is(err, catalog.ErrSkillSymlink):
		return newAgentStatusError(http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, catalog.ErrInvalidSkillKey), errors.Is(err, catalog.ErrInvalidSkillPath), errors.Is(err, catalog.ErrSkillIsDirectory), errors.Is(err, catalog.ErrSkillDirectoryNotEmpty):
		return newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	default:
		message := err.Error()
		switch {
		case strings.Contains(message, "not found"):
			return newAgentStatusError(http.StatusNotFound, "not_found", message)
		case strings.Contains(message, "already exists"):
			return newAgentStatusError(http.StatusConflict, "conflict", message)
		default:
			return newAgentStatusError(http.StatusBadRequest, "invalid_request", message)
		}
	}
}

func parseLooseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
