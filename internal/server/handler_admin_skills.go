package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
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
