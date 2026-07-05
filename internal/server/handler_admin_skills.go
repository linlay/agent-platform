package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
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

func (s *Server) handleAdminSkillsV2(w http.ResponseWriter, r *http.Request) {
	response, err := s.listAdminSkillsV2()
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) listAdminSkillsV2() ([]api.AdminSkillV2Summary, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return nil, err
	}
	items, err := registry.AdminSkills()
	if err != nil {
		return nil, err
	}
	response := make([]api.AdminSkillV2Summary, 0, len(items))
	for _, item := range items {
		response = append(response, buildAdminSkillV2Summary(item))
	}
	return response, nil
}

func (s *Server) handleAdminSkillV2Detail(w http.ResponseWriter, r *http.Request) {
	key, err := queryOrBodyIDAny(r, []string{"key", "skillKey"})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	if strings.TrimSpace(key) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "key or skillKey is required"))
		return
	}
	response, err := s.adminSkillV2Detail(key, r.URL.Query().Get("openPath"))
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillV2Create(w http.ResponseWriter, r *http.Request) {
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
	response, err := s.adminSkillV2Detail(created.Key, "SKILL.md")
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillV2Delete(w http.ResponseWriter, r *http.Request) {
	s.handleAdminSkillDelete(w, r)
}

func (s *Server) handleAdminSkillV2File(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		response, err := s.readAdminSkillFileV2(r.URL.Query().Get("key"), r.URL.Query().Get("path"))
		s.writeAgentHTTPResponse(w, response, err)
	case http.MethodPut:
		var req api.WriteAdminSkillFileRequest
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
			return
		}
		response, err := s.writeAdminSkillFileV2(r.Context(), req)
		s.writeAgentHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func (s *Server) handleAdminSkillV2FileCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateAdminSkillV2FileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.createAdminSkillFileV2(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillV2FileMkdir(w http.ResponseWriter, r *http.Request) {
	var req api.MkdirAdminSkillFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.mkdirAdminSkillFileV2(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillV2FileRename(w http.ResponseWriter, r *http.Request) {
	var req api.RenameAdminSkillFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.renameAdminSkillFileV2(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillV2FileDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteAdminSkillFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.deleteAdminSkillFileV2(r.Context(), req)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillV2FileUpload(w http.ResponseWriter, r *http.Request) {
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
	response, err := s.uploadAdminSkillFileV2(r.Context(), key, relPath, file, overwrite)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillV2Validate(w http.ResponseWriter, r *http.Request) {
	var req api.ValidateAdminSkillV2Request
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	if req.Key == "" {
		req.Key = r.URL.Query().Get("key")
	}
	response, err := s.validateAdminSkillV2(r.Context(), req.Key)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillDetail(w http.ResponseWriter, r *http.Request) {
	key, err := queryOrBodyIDAny(r, []string{"key", "skillKey"})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	if strings.TrimSpace(key) == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "key or skillKey is required"))
		return
	}
	response, err := s.adminSkillDetail(key)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillCreate(w http.ResponseWriter, r *http.Request) {
	var req api.CreateAdminSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.createAdminSkill(r.Context(), req)
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

func (s *Server) handleAdminSkillFileDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteAdminSkillFileRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.deleteAdminSkillFile(r.Context(), req)
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
	registry, err := s.adminSkillRegistry()
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	metadata, err := registry.UploadEditableSkillFile(key, relPath, file, overwrite)
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, mapSkillEditError(err))
		return
	}
	if err := s.reloadAdminSkills(r.Context()); err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	response := api.AdminSkillFileMutationResponse{
		Key:      strings.TrimSpace(key),
		Path:     metadata.Path,
		Created:  true,
		File:     apiAdminSkillFile(metadata),
		Reloaded: true,
	}
	writeJSON(w, http.StatusOK, api.Success(response))
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

func (s *Server) adminSkillDetail(key string) (api.AdminSkillDetailResponse, error) {
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
	return buildAdminSkillDetail(item), nil
}

func (s *Server) createAdminSkill(ctx context.Context, req api.CreateAdminSkillRequest) (api.AdminSkillDetailResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillDetailResponse{}, err
	}
	files := make([]catalog.EditableSkillInlineFile, 0, len(req.Files))
	for _, file := range req.Files {
		files = append(files, catalog.EditableSkillInlineFile{
			Path:     file.Path,
			Content:  file.Content,
			Encoding: file.Encoding,
		})
	}
	item, err := registry.CreateEditableSkill(req.Key, req.SkillMd, files)
	if err != nil {
		return api.AdminSkillDetailResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillDetailResponse{}, err
	}
	if refreshed, found, err := registry.AdminSkill(item.Key); err == nil && found {
		item = refreshed
	}
	return buildAdminSkillDetail(item), nil
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

func (s *Server) readAdminSkillFile(key string, relPath string) (api.AdminSkillFileResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillFileResponse{}, err
	}
	file, err := registry.ReadEditableSkillFile(key, relPath)
	if err != nil {
		return api.AdminSkillFileResponse{}, mapSkillEditError(err)
	}
	return api.AdminSkillFileResponse{
		Key:       file.Key,
		Path:      file.Path,
		Content:   file.Content,
		Encoding:  file.Encoding,
		SHA256:    file.SHA256,
		Size:      file.Size,
		UpdatedAt: file.UpdatedAt,
	}, nil
}

func (s *Server) writeAdminSkillFile(ctx context.Context, req api.WriteAdminSkillFileRequest) (api.AdminSkillFileMutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillFileMutationResponse{}, err
	}
	file, err := registry.WriteEditableSkillFile(req.Key, req.Path, req.Content, req.Encoding, req.BaseSHA256)
	if err != nil {
		return api.AdminSkillFileMutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillFileMutationResponse{}, err
	}
	usage, _ := registry.EditableSkillUsage(req.Key)
	return api.AdminSkillFileMutationResponse{
		Key:          strings.TrimSpace(req.Key),
		Path:         file.Path,
		Updated:      true,
		File:         apiAdminSkillFile(file),
		Reloaded:     true,
		UsedByAgents: usage,
	}, nil
}

func (s *Server) deleteAdminSkillFile(ctx context.Context, req api.DeleteAdminSkillFileRequest) (api.AdminSkillFileMutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillFileMutationResponse{}, err
	}
	if err := registry.DeleteEditableSkillFile(req.Key, req.Path, req.Recursive, req.BaseSHA256); err != nil {
		return api.AdminSkillFileMutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillFileMutationResponse{}, err
	}
	usage, _ := registry.EditableSkillUsage(req.Key)
	return api.AdminSkillFileMutationResponse{
		Key:          strings.TrimSpace(req.Key),
		Path:         strings.TrimSpace(req.Path),
		Deleted:      true,
		Reloaded:     true,
		UsedByAgents: usage,
	}, nil
}

func (s *Server) mkdirAdminSkillFile(ctx context.Context, req api.MkdirAdminSkillFileRequest) (api.AdminSkillFileMutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillFileMutationResponse{}, err
	}
	file, err := registry.MkdirEditableSkillFile(req.Key, req.Path)
	if err != nil {
		return api.AdminSkillFileMutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillFileMutationResponse{}, err
	}
	return api.AdminSkillFileMutationResponse{
		Key:      strings.TrimSpace(req.Key),
		Path:     file.Path,
		Created:  true,
		File:     apiAdminSkillFile(file),
		Reloaded: true,
	}, nil
}

func (s *Server) renameAdminSkillFile(ctx context.Context, req api.RenameAdminSkillFileRequest) (api.AdminSkillFileMutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillFileMutationResponse{}, err
	}
	file, err := registry.RenameEditableSkillFile(req.Key, req.FromPath, req.ToPath, req.Overwrite)
	if err != nil {
		return api.AdminSkillFileMutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillFileMutationResponse{}, err
	}
	return api.AdminSkillFileMutationResponse{
		Key:      strings.TrimSpace(req.Key),
		FromPath: strings.TrimSpace(req.FromPath),
		ToPath:   file.Path,
		Renamed:  true,
		File:     apiAdminSkillFile(file),
		Reloaded: true,
	}, nil
}

func (s *Server) adminSkillV2Detail(key string, openPath string) (api.AdminSkillV2DetailResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillV2DetailResponse{}, err
	}
	item, found, err := registry.AdminSkill(key)
	if err != nil {
		return api.AdminSkillV2DetailResponse{}, mapSkillEditError(err)
	}
	if !found {
		return api.AdminSkillV2DetailResponse{}, newAgentStatusError(http.StatusNotFound, "not_found", "skill not found")
	}
	response := buildAdminSkillV2Detail(item)
	openPath = strings.TrimSpace(openPath)
	if openPath == "" {
		return response, nil
	}
	entry := findAdminSkillV2Entry(response.FileManifest.Entries, openPath)
	if entry == nil || entry.ContentKind != "text" || !entry.Editable {
		return response, nil
	}
	file, err := registry.ReadEditableSkillFile(item.Key, openPath)
	if err != nil {
		return api.AdminSkillV2DetailResponse{}, mapSkillEditError(err)
	}
	response.OpenedFile = apiAdminSkillV2TextFile(file, true)
	return response, nil
}

func (s *Server) readAdminSkillFileV2(key string, relPath string) (api.AdminSkillV2TextFile, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillV2TextFile{}, err
	}
	file, err := registry.ReadEditableSkillFile(key, relPath)
	if err != nil {
		return api.AdminSkillV2TextFile{}, mapSkillEditError(err)
	}
	text := apiAdminSkillV2TextFile(file, true)
	if text == nil {
		return api.AdminSkillV2TextFile{}, newAgentStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", "file is not editable text")
	}
	return *text, nil
}

func (s *Server) writeAdminSkillFileV2(ctx context.Context, req api.WriteAdminSkillFileRequest) (api.AdminSkillV2MutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	file, err := registry.WriteEditableSkillFile(req.Key, req.Path, req.Content, req.Encoding, req.BaseSHA256)
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	item, err := adminSkillV2Item(registry, req.Key)
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	opened := apiAdminSkillV2TextFile(catalog.EditableSkillFileContent{
		Key:       strings.TrimSpace(req.Key),
		Path:      file.Path,
		Content:   req.Content,
		Encoding:  firstNonBlank(req.Encoding, "utf-8"),
		SHA256:    file.SHA256,
		Size:      file.Size,
		UpdatedAt: file.UpdatedAt,
	}, true)
	return buildAdminSkillV2Mutation(item, "save", file.Path, opened, false), nil
}

func (s *Server) createAdminSkillFileV2(ctx context.Context, req api.CreateAdminSkillV2FileRequest) (api.AdminSkillV2MutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	if _, _, err := registry.ResolveEditableSkillFile(req.Key, req.Path); err == nil {
		return api.AdminSkillV2MutationResponse{}, mapSkillEditError(catalog.ErrSkillConflict)
	} else if !errors.Is(err, catalog.ErrSkillNotFound) {
		return api.AdminSkillV2MutationResponse{}, mapSkillEditError(err)
	}
	file, err := registry.WriteEditableSkillFile(req.Key, req.Path, req.Content, req.Encoding, "")
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	item, err := adminSkillV2Item(registry, req.Key)
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	opened := apiAdminSkillV2TextFile(catalog.EditableSkillFileContent{
		Key:       strings.TrimSpace(req.Key),
		Path:      file.Path,
		Content:   req.Content,
		Encoding:  firstNonBlank(req.Encoding, "utf-8"),
		SHA256:    file.SHA256,
		Size:      file.Size,
		UpdatedAt: file.UpdatedAt,
	}, true)
	return buildAdminSkillV2Mutation(item, "create", file.Path, opened, true), nil
}

func (s *Server) mkdirAdminSkillFileV2(ctx context.Context, req api.MkdirAdminSkillFileRequest) (api.AdminSkillV2MutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	file, err := registry.MkdirEditableSkillFile(req.Key, req.Path)
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	item, err := adminSkillV2Item(registry, req.Key)
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	return buildAdminSkillV2Mutation(item, "mkdir", file.Path, nil, true), nil
}

func (s *Server) renameAdminSkillFileV2(ctx context.Context, req api.RenameAdminSkillFileRequest) (api.AdminSkillV2MutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	file, err := registry.RenameEditableSkillFile(req.Key, req.FromPath, req.ToPath, req.Overwrite)
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	item, err := adminSkillV2Item(registry, req.Key)
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	return buildAdminSkillV2Mutation(item, "rename", file.Path, nil, true), nil
}

func (s *Server) deleteAdminSkillFileV2(ctx context.Context, req api.DeleteAdminSkillFileRequest) (api.AdminSkillV2MutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	if err := registry.DeleteEditableSkillFile(req.Key, req.Path, req.Recursive, req.BaseSHA256); err != nil {
		return api.AdminSkillV2MutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	item, err := adminSkillV2Item(registry, req.Key)
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	manifest := buildAdminSkillV2Manifest(item.Files)
	return buildAdminSkillV2Mutation(item, "delete", manifest.DefaultOpenPath, nil, true), nil
}

func (s *Server) uploadAdminSkillFileV2(ctx context.Context, key string, relPath string, src io.Reader, overwrite bool) (api.AdminSkillV2MutationResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	file, err := registry.UploadEditableSkillFile(key, relPath, src, overwrite)
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	item, err := adminSkillV2Item(registry, key)
	if err != nil {
		return api.AdminSkillV2MutationResponse{}, err
	}
	return buildAdminSkillV2Mutation(item, "upload", file.Path, nil, true), nil
}

func (s *Server) validateAdminSkillV2(ctx context.Context, key string) (api.AdminSkillV2ValidateResponse, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return api.AdminSkillV2ValidateResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "key is required")
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillV2ValidateResponse{}, err
	}
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillV2ValidateResponse{}, err
	}
	item, found, err := registry.AdminSkill(key)
	if err != nil {
		return api.AdminSkillV2ValidateResponse{}, mapSkillEditError(err)
	}
	if !found {
		return api.AdminSkillV2ValidateResponse{}, newAgentStatusError(http.StatusNotFound, "not_found", "skill not found")
	}
	return api.AdminSkillV2ValidateResponse{
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
		if err := s.deps.Registry.Reload(ctx, "agents"); err != nil {
			return err
		}
	}
	s.broadcast("catalog.updated", map[string]any{
		"reason":    "skills",
		"timestamp": time.Now().UnixMilli(),
	})
	return nil
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

func buildAdminSkillDetail(item catalog.AdminSkill) api.AdminSkillDetailResponse {
	response := api.AdminSkillDetailResponse{
		AdminSkillSummary: buildAdminSkillSummary(item),
		Source: &api.AgentSource{
			Kind:     item.Source.Kind,
			Path:     item.Source.Path,
			AgentDir: item.Source.SkillDir,
		},
		Diagnostics: adminSkillDiagnostics(item.Diagnostics),
		SkillMd:     item.SkillMd,
		Files:       make([]api.AdminSkillFile, 0, len(item.Files)),
	}
	for _, file := range item.Files {
		response.Files = append(response.Files, *apiAdminSkillFile(file))
	}
	return response
}

func buildAdminSkillV2Detail(item catalog.AdminSkill) api.AdminSkillV2DetailResponse {
	return api.AdminSkillV2DetailResponse{
		SchemaVersion: 2,
		Skill:         buildAdminSkillV2Summary(item),
		Capabilities:  buildAdminSkillV2Capabilities(),
		FileManifest:  buildAdminSkillV2Manifest(item.Files),
		Diagnostics:   adminSkillDiagnostics(item.Diagnostics),
	}
}

func buildAdminSkillV2Summary(item catalog.AdminSkill) api.AdminSkillV2Summary {
	summary := api.AdminSkillV2Summary{
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

func buildAdminSkillV2Capabilities() api.AdminSkillV2Capabilities {
	return api.AdminSkillV2Capabilities{
		MaxTextBytes:   catalog.EditableSkillMaxTextBytes,
		MaxUploadBytes: catalog.EditableSkillMaxUploadBytes,
		CanCreate:      true,
		CanRename:      true,
		CanDelete:      true,
		CanUpload:      true,
		CanDownload:    true,
	}
}

func buildAdminSkillV2Manifest(files []catalog.EditableSkillFile) api.AdminSkillV2FileManifest {
	entryByPath := map[string]api.AdminSkillV2FileEntry{}
	for _, file := range files {
		entry := adminSkillV2EntryFromFile(file)
		entryByPath[entry.Path] = entry
		for parent := entry.ParentPath; parent != ""; parent = adminSkillParentPath(parent) {
			if _, exists := entryByPath[parent]; exists {
				continue
			}
			entryByPath[parent] = api.AdminSkillV2FileEntry{
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
			return adminSkillV2EntryLess(entryByPath[children[parent][i]], entryByPath[children[parent][j]], parent == "")
		})
	}

	entries := make([]api.AdminSkillV2FileEntry, 0, len(entryByPath))
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

	var counts api.AdminSkillV2FileCounts
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

	return api.AdminSkillV2FileManifest{
		Revision:        adminSkillV2ManifestRevision(entries),
		DefaultOpenPath: defaultOpenPath,
		Counts:          counts,
		Entries:         entries,
	}
}

func adminSkillV2EntryFromFile(file catalog.EditableSkillFile) api.AdminSkillV2FileEntry {
	contentKind := "binary"
	if file.Kind == "directory" {
		contentKind = "directory"
	} else if file.Text {
		contentKind = "text"
	}
	role := adminSkillV2FileRole(file.Path)
	editable := file.Kind == "file" && contentKind == "text"
	return api.AdminSkillV2FileEntry{
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
		Language:     adminSkillV2FileLanguage(file.Path, contentKind),
		Role:         role,
		Editable:     editable,
		Downloadable: file.Kind == "file",
		Uploadable:   file.Kind == "file",
		Renamable:    file.Path != "SKILL.md",
		Deletable:    file.Path != "SKILL.md",
	}
}

func adminSkillV2EntryLess(a api.AdminSkillV2FileEntry, b api.AdminSkillV2FileEntry, root bool) bool {
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

func adminSkillRootPriority(entry api.AdminSkillV2FileEntry) int {
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

func adminSkillV2FileRole(relPath string) string {
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

func adminSkillV2FileLanguage(relPath string, contentKind string) string {
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

func adminSkillV2ManifestRevision(entries []api.AdminSkillV2FileEntry) string {
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

func findAdminSkillV2Entry(entries []api.AdminSkillV2FileEntry, relPath string) *api.AdminSkillV2FileEntry {
	relPath = strings.TrimSpace(relPath)
	for i := range entries {
		if entries[i].Path == relPath {
			return &entries[i]
		}
	}
	return nil
}

func apiAdminSkillV2TextFile(file catalog.EditableSkillFileContent, editable bool) *api.AdminSkillV2TextFile {
	encoding := firstNonBlank(file.Encoding, "utf-8")
	return &api.AdminSkillV2TextFile{
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

func adminSkillV2Item(registry adminSkillRegistry, key string) (catalog.AdminSkill, error) {
	item, found, err := registry.AdminSkill(key)
	if err != nil {
		return catalog.AdminSkill{}, mapSkillEditError(err)
	}
	if !found {
		return catalog.AdminSkill{}, newAgentStatusError(http.StatusNotFound, "not_found", "skill not found")
	}
	return item, nil
}

func buildAdminSkillV2Mutation(item catalog.AdminSkill, action string, selectedPath string, opened *api.AdminSkillV2TextFile, includeManifest bool) api.AdminSkillV2MutationResponse {
	manifest := buildAdminSkillV2Manifest(item.Files)
	if selectedPath == "" {
		selectedPath = manifest.DefaultOpenPath
	}
	entry := findAdminSkillV2Entry(manifest.Entries, selectedPath)
	skill := buildAdminSkillV2Summary(item)
	response := api.AdminSkillV2MutationResponse{
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

func apiAdminSkillFile(file catalog.EditableSkillFile) *api.AdminSkillFile {
	return &api.AdminSkillFile{
		Path:      file.Path,
		Name:      file.Name,
		Kind:      file.Kind,
		Size:      file.Size,
		UpdatedAt: file.UpdatedAt,
		MimeType:  file.MimeType,
		Text:      file.Text,
		Binary:    file.Binary,
		SHA256:    file.SHA256,
	}
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
