package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/documentpreview"
)

func (s *Server) handleDocumentPreviewCapabilities(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cfg := s.deps.Config.DocumentPreview
	if cfg.Provider == "" {
		cfg = documentpreview.DefaultConfig()
	}
	writeJSON(w, http.StatusOK, api.Success(cfg.Capabilities()))
}

func (s *Server) handleDocumentPreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.documentPreview == nil {
		writeJSON(w, 503, api.Failure(503, "未配置在线预览服务", map[string]any{"code": "preview_disabled"}))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	defer r.Body.Close()
	var request documentpreview.Request
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || ensureJSONEOF(decoder) != nil {
		writeJSON(w, 400, api.Failure(400, "invalid document preview request"))
		return
	}
	p := PrincipalFromContext(r.Context())
	subject := "local"
	if p != nil {
		subject = p.Subject
	}
	result, err := s.documentPreview.Prepare(r.Context(), subject, request, func() (documentpreview.Resolved, error) { return s.resolvePreviewSource(r, request.Source) })
	if err != nil {
		var previewErr *documentpreview.Error
		if errors.As(err, &previewErr) {
			writeJSON(w, previewErr.Status, api.Failure(previewErr.Status, previewErr.Message, map[string]any{"code": previewErr.Code}))
			return
		}
		writeJSON(w, 500, api.Failure(500, "无法准备在线预览"))
		return
	}
	writeJSON(w, http.StatusOK, api.Success(result))
}

func (s *Server) resolvePreviewSource(r *http.Request, source documentpreview.Source) (documentpreview.Resolved, error) {
	invalid := func() (documentpreview.Resolved, error) {
		return documentpreview.Resolved{}, &documentpreview.Error{Status: 400, Code: "invalid_source", Message: "文件来源无效"}
	}
	switch source.Kind {
	case "workspace-file":
		if source.ChatID != "" || source.RelativePath != "" || len(source.Path) > 4096 || len(source.AgentKey) > 512 {
			return invalid()
		}
		file, err := s.resolveAgentFile(source.AgentKey, source.Path)
		if err != nil {
			return documentpreview.Resolved{}, &documentpreview.Error{Status: 403, Code: "preview_source_denied", Message: "工作区文件不存在或不可读取"}
		}
		return documentpreview.Resolved{Path: file.AbsolutePath, Identity: "workspace:" + file.AgentKey + ":" + file.AbsolutePath}, nil
	case "chat-resource":
		if source.AgentKey != "" || source.Path != "" || len(source.RelativePath) > 4096 || strings.Contains(source.RelativePath, "\\") {
			return invalid()
		}
		key, err := chat.BuildResourceKey(source.ChatID, source.RelativePath)
		if err != nil {
			return invalid()
		}
		if !s.principalCanAccessResourceChat(PrincipalFromContext(r.Context()), source.ChatID) {
			return documentpreview.Resolved{}, &documentpreview.Error{Status: 403, Code: "preview_source_denied", Message: "没有读取此聊天文件的权限"}
		}
		path, err := s.resolveResourcePath(source.ChatID, source.RelativePath, key)
		if err != nil {
			status := 403
			if errors.Is(err, os.ErrNotExist) {
				status = 404
			}
			return documentpreview.Resolved{}, &documentpreview.Error{Status: status, Code: "preview_source_denied", Message: "聊天文件不存在或不可读取"}
		}
		return documentpreview.Resolved{Path: path, Identity: "chat:" + source.ChatID + ":" + path}, nil
	default:
		return invalid()
	}
}
