package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"agent-platform/internal/api"
	"agent-platform/internal/automation"
	"agent-platform/internal/catalog"
)

const adminSourceMaxTextBytes int64 = 1 << 20

type editableAdminAgentSourceRegistry interface {
	ReadEditableAgentSource(key string) (catalog.EditableAgentSourceFile, error)
	WriteEditableAgentSource(key string, content string, baseSHA256 string) (catalog.EditableAgentSourceFile, error)
}

// handleAdminSource exposes the common admin text-source contract. Source
// targets are logical catalog identifiers; filesystem paths remain server-side.
func (s *Server) handleAdminSource(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		target, err := adminSourceTargetFromQuery(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
			return
		}
		response, err := s.readAdminSource(target)
		s.writeAgentHTTPResponse(w, response, err)
	case http.MethodPut:
		var req api.UpdateAdminSourceRequest
		if err := decodeJSON(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
			return
		}
		target, err := normalizeAdminSourceTarget(req.Target)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
			return
		}
		response, err := s.writeAdminSource(r.Context(), target, req.Content, req.BaseSHA256)
		s.writeAgentHTTPResponse(w, response, err)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}

func adminSourceTargetFromQuery(r *http.Request) (api.AdminSourceTarget, error) {
	query := r.URL.Query()
	return normalizeAdminSourceTarget(api.AdminSourceTarget{
		Type:     query.Get("type"),
		Key:      query.Get("key"),
		Path:     query.Get("path"),
		Category: query.Get("category"),
		File:     query.Get("file"),
	})
}

func normalizeAdminSourceTarget(target api.AdminSourceTarget) (api.AdminSourceTarget, error) {
	target.Type = strings.ToLower(strings.TrimSpace(target.Type))
	target.Key = strings.TrimSpace(target.Key)
	target.Path = strings.TrimSpace(target.Path)
	target.Category = strings.TrimSpace(target.Category)
	target.File = strings.TrimSpace(target.File)

	switch target.Type {
	case "agent", "automation":
		if target.Key == "" {
			return api.AdminSourceTarget{}, fmt.Errorf("key is required for %s source", target.Type)
		}
		if target.Path != "" || target.Category != "" || target.File != "" {
			return api.AdminSourceTarget{}, fmt.Errorf("unexpected target fields for %s source", target.Type)
		}
	case "skill":
		if target.Key == "" || target.Path == "" {
			return api.AdminSourceTarget{}, fmt.Errorf("key and path are required for skill source")
		}
		if target.Category != "" || target.File != "" {
			return api.AdminSourceTarget{}, fmt.Errorf("unexpected target fields for skill source")
		}
	case "registry":
		if target.Category == "" || target.File == "" {
			return api.AdminSourceTarget{}, fmt.Errorf("category and file are required for registry source")
		}
		if target.Key != "" || target.Path != "" {
			return api.AdminSourceTarget{}, fmt.Errorf("unexpected target fields for registry source")
		}
	default:
		return api.AdminSourceTarget{}, fmt.Errorf("unsupported source type")
	}
	return target, nil
}

func (s *Server) readAdminSource(target api.AdminSourceTarget) (api.AdminSourceResponse, error) {
	switch target.Type {
	case "agent":
		return s.readAdminAgentTextSource(target)
	case "skill":
		return s.readAdminSkillTextSource(target)
	case "automation":
		return s.readAdminAutomationTextSource(target)
	case "registry":
		return s.readAdminRegistryTextSource(target)
	default:
		return api.AdminSourceResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "unsupported source type")
	}
}

func (s *Server) writeAdminSource(ctx context.Context, target api.AdminSourceTarget, content string, baseSHA256 string) (api.AdminSourceResponse, error) {
	switch target.Type {
	case "agent":
		return s.writeAdminAgentTextSource(ctx, target, content, baseSHA256)
	case "skill":
		return s.writeAdminSkillTextSource(ctx, target, content, baseSHA256)
	case "automation":
		return s.writeAdminAutomationTextSource(target, content, baseSHA256)
	case "registry":
		return s.writeAdminRegistryTextSource(target, content, baseSHA256)
	default:
		return api.AdminSourceResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "unsupported source type")
	}
}

func (s *Server) adminAgentSourceEditor() (editableAdminAgentSourceRegistry, error) {
	registry, ok := s.deps.Registry.(editableAdminAgentSourceRegistry)
	if !ok || registry == nil {
		return nil, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent source editing is not configured")
	}
	return registry, nil
}

func (s *Server) readAdminAgentTextSource(target api.AdminSourceTarget) (api.AdminSourceResponse, error) {
	editor, err := s.adminAgentSourceEditor()
	if err != nil {
		return api.AdminSourceResponse{}, err
	}
	file, err := editor.ReadEditableAgentSource(target.Key)
	if err != nil {
		return api.AdminSourceResponse{}, mapAdminSourceAgentError(err)
	}
	if err := requireAdminAgentYAMLSource(file); err != nil {
		return api.AdminSourceResponse{}, err
	}
	return adminSourceFromAgentFile(target, file), nil
}

func (s *Server) writeAdminAgentTextSource(ctx context.Context, target api.AdminSourceTarget, content string, baseSHA256 string) (api.AdminSourceResponse, error) {
	editor, err := s.adminAgentSourceEditor()
	if err != nil {
		return api.AdminSourceResponse{}, err
	}
	current, err := editor.ReadEditableAgentSource(target.Key)
	if err != nil {
		return api.AdminSourceResponse{}, mapAdminSourceAgentError(err)
	}
	if err := requireAdminAgentYAMLSource(current); err != nil {
		return api.AdminSourceResponse{}, err
	}
	file, err := editor.WriteEditableAgentSource(target.Key, content, baseSHA256)
	if err != nil {
		return api.AdminSourceResponse{}, mapAdminSourceAgentError(err)
	}
	if err := s.reloadAgentCatalog(ctx); err != nil {
		return api.AdminSourceResponse{}, err
	}
	return adminSourceFromAgentFile(target, file), nil
}

func requireAdminAgentYAMLSource(file catalog.EditableAgentSourceFile) error {
	source := file.Source
	agentDir := strings.TrimSpace(source.AgentDir)
	path := strings.TrimSpace(source.Path)
	if source.Kind != "directory" || agentDir == "" || filepath.Base(path) != "agent.yml" || filepath.Clean(path) != filepath.Join(filepath.Clean(agentDir), "agent.yml") {
		return newAgentStatusError(http.StatusForbidden, "forbidden", "agent source editing only supports directory agent.yml")
	}
	return nil
}

func adminSourceFromAgentFile(target api.AdminSourceTarget, file catalog.EditableAgentSourceFile) api.AdminSourceResponse {
	return api.AdminSourceResponse{
		Target: target,
		Source: api.AgentSource{
			Kind:     file.Source.Kind,
			Path:     file.Source.Path,
			AgentDir: file.Source.AgentDir,
		},
		Content:   file.Content,
		Encoding:  file.Encoding,
		SHA256:    file.SHA256,
		Size:      file.Size,
		UpdatedAt: file.UpdatedAt,
	}
}

func mapAdminSourceAgentError(err error) error {
	switch {
	case errors.Is(err, catalog.ErrAgentSourceNotFound):
		return newAgentStatusError(http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, catalog.ErrAgentSourceConflict):
		return newAgentStatusError(http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, catalog.ErrAgentSourceTooLarge):
		return newAgentStatusError(http.StatusRequestEntityTooLarge, "payload_too_large", err.Error())
	case errors.Is(err, catalog.ErrAgentSourceBinary):
		return newAgentStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", err.Error())
	case errors.Is(err, catalog.ErrAgentSourceSymlink):
		return newAgentStatusError(http.StatusForbidden, "forbidden", err.Error())
	default:
		return mapAgentEditError(err)
	}
}

func (s *Server) readAdminSkillTextSource(target api.AdminSourceTarget) (api.AdminSourceResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSourceResponse{}, err
	}
	file, err := registry.ReadEditableSkillFile(target.Key, target.Path)
	if err != nil {
		return api.AdminSourceResponse{}, mapSkillEditError(err)
	}
	pathOnDisk, _, err := registry.ResolveEditableSkillFile(target.Key, target.Path)
	if err != nil {
		return api.AdminSourceResponse{}, mapSkillEditError(err)
	}
	return adminSourceFromSkillFile(target, pathOnDisk, file), nil
}

func (s *Server) writeAdminSkillTextSource(ctx context.Context, target api.AdminSourceTarget, content string, baseSHA256 string) (api.AdminSourceResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSourceResponse{}, err
	}
	if _, err := registry.WriteEditableSkillFile(target.Key, target.Path, content, "utf-8", baseSHA256); err != nil {
		return api.AdminSourceResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSourceResponse{}, err
	}
	return s.readAdminSkillTextSource(target)
}

func adminSourceFromSkillFile(target api.AdminSourceTarget, pathOnDisk string, file catalog.EditableSkillFileContent) api.AdminSourceResponse {
	return api.AdminSourceResponse{
		Target:    target,
		Source:    api.AgentSource{Kind: "skills-market", Path: pathOnDisk},
		Content:   file.Content,
		Encoding:  file.Encoding,
		SHA256:    file.SHA256,
		Size:      file.Size,
		UpdatedAt: file.UpdatedAt,
	}
}

func (s *Server) readAdminAutomationTextSource(target api.AdminSourceTarget) (api.AdminSourceResponse, error) {
	registry := s.deps.AutomationRegistry
	if registry == nil {
		return api.AdminSourceResponse{}, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "automation registry is not configured")
	}
	file, err := registry.ReadEditableSource(target.Key)
	if err != nil {
		return api.AdminSourceResponse{}, mapAdminSourceAutomationError(err)
	}
	return adminSourceFromAutomationFile(target, file), nil
}

func (s *Server) writeAdminAutomationTextSource(target api.AdminSourceTarget, content string, baseSHA256 string) (api.AdminSourceResponse, error) {
	registry := s.deps.AutomationRegistry
	if registry == nil {
		return api.AdminSourceResponse{}, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "automation registry is not configured")
	}
	file, err := registry.WriteEditableSource(target.Key, content, baseSHA256)
	if err != nil {
		return api.AdminSourceResponse{}, mapAdminSourceAutomationError(err)
	}
	if err := s.reloadAutomations(); err != nil {
		return api.AdminSourceResponse{}, err
	}
	return adminSourceFromAutomationFile(target, file), nil
}

func adminSourceFromAutomationFile(target api.AdminSourceTarget, file automation.EditableSourceFile) api.AdminSourceResponse {
	return api.AdminSourceResponse{
		Target:    target,
		Source:    api.AgentSource{Kind: "automation", Path: file.Path},
		Content:   file.Content,
		Encoding:  file.Encoding,
		SHA256:    file.SHA256,
		Size:      file.Size,
		UpdatedAt: file.UpdatedAt,
	}
}

func mapAdminSourceAutomationError(err error) error {
	switch {
	case errors.Is(err, automation.ErrSourceNotFound):
		return newAgentStatusError(http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, automation.ErrSourceConflict):
		return newAgentStatusError(http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, automation.ErrSourceTooLarge):
		return newAgentStatusError(http.StatusRequestEntityTooLarge, "payload_too_large", err.Error())
	case errors.Is(err, automation.ErrSourceBinary):
		return newAgentStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", err.Error())
	case errors.Is(err, automation.ErrSourceSymlink):
		return newAgentStatusError(http.StatusForbidden, "forbidden", err.Error())
	default:
		return newAgentStatusError(http.StatusBadRequest, "invalid_request", err.Error())
	}
}

func (s *Server) readAdminRegistryTextSource(target api.AdminSourceTarget) (api.AdminSourceResponse, error) {
	path, err := s.adminRegistryFilePath(target.Category, target.File)
	if err != nil {
		return api.AdminSourceResponse{}, err
	}
	content, sha, size, updatedAt, err := readAdminSourceTextFile(path)
	if err != nil {
		return api.AdminSourceResponse{}, mapAdminRegistrySourceReadError(err)
	}
	return api.AdminSourceResponse{
		Target:    target,
		Source:    api.AgentSource{Kind: "registry", Path: path},
		Content:   content,
		Encoding:  "utf-8",
		SHA256:    sha,
		Size:      size,
		UpdatedAt: updatedAt,
	}, nil
}

func (s *Server) writeAdminRegistryTextSource(target api.AdminSourceTarget, content string, baseSHA256 string) (api.AdminSourceResponse, error) {
	path, err := s.adminRegistryFilePath(target.Category, target.File)
	if err != nil {
		return api.AdminSourceResponse{}, err
	}
	data := []byte(content)
	if int64(len(data)) > adminSourceMaxTextBytes {
		return api.AdminSourceResponse{}, newAgentStatusError(http.StatusRequestEntityTooLarge, "payload_too_large", "registry source file too large")
	}
	if !utf8.Valid(data) {
		return api.AdminSourceResponse{}, newAgentStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", "registry source file is not utf-8 text")
	}
	_, _, syntaxOK := s.analyzeAdminRegistry(target.Category, target.File, data, path)
	if !syntaxOK {
		return api.AdminSourceResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_yaml", "invalid registry yaml")
	}

	s.adminSourceMu.Lock()
	defer s.adminSourceMu.Unlock()
	if _, currentSHA, _, _, err := readAdminSourceTextFile(path); err == nil {
		if expected := strings.TrimSpace(baseSHA256); expected != "" && expected != currentSHA {
			return api.AdminSourceResponse{}, newAgentStatusError(http.StatusConflict, "conflict", "registry source conflict")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return api.AdminSourceResponse{}, mapAdminRegistrySourceReadError(err)
	} else if strings.TrimSpace(baseSHA256) != "" {
		return api.AdminSourceResponse{}, newAgentStatusError(http.StatusConflict, "conflict", "registry source conflict")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return api.AdminSourceResponse{}, err
	}
	if err := atomicWriteAdminRegistryFile(path, data); err != nil {
		return api.AdminSourceResponse{}, err
	}
	return s.readAdminRegistryTextSource(target)
}

func readAdminSourceTextFile(path string) (string, string, int64, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", 0, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", 0, 0, fmt.Errorf("registry source path is a symlink")
	}
	if !info.Mode().IsRegular() {
		return "", "", 0, 0, fmt.Errorf("registry source is not a regular file")
	}
	if info.Size() > adminSourceMaxTextBytes {
		return "", "", 0, 0, fmt.Errorf("registry source file too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", 0, 0, err
	}
	if !utf8.Valid(data) {
		return "", "", 0, 0, fmt.Errorf("registry source file is not utf-8 text")
	}
	sum := sha256.Sum256(data)
	return string(data), fmt.Sprintf("%x", sum), info.Size(), info.ModTime().UnixMilli(), nil
}

func mapAdminRegistrySourceReadError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return newAgentStatusError(http.StatusNotFound, "not_found", "registry file not found")
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "too large"):
		return newAgentStatusError(http.StatusRequestEntityTooLarge, "payload_too_large", message)
	case strings.Contains(message, "utf-8"):
		return newAgentStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", message)
	case strings.Contains(message, "symlink"):
		return newAgentStatusError(http.StatusForbidden, "forbidden", message)
	default:
		return newAgentStatusError(http.StatusBadRequest, "invalid_request", message)
	}
}
