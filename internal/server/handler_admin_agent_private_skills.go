package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

type adminAgentPrivateSkillRegistry interface {
	BeginImportEditableAgentPrivateSkillArchive(agentKey, key string, source io.ReaderAt, size int64, confirmCenterOverride bool) (*catalog.EditableAgentPrivateSkillMutation, error)
	BeginDeleteEditableAgentPrivateSkill(agentKey, key string) (*catalog.EditableAgentPrivateSkillMutation, error)
	RollbackEditableAgentPrivateSkillMutation(mutation *catalog.EditableAgentPrivateSkillMutation) error
	CommitEditableAgentPrivateSkillMutation(mutation *catalog.EditableAgentPrivateSkillMutation) error
}

func (s *Server) adminAgentPrivateSkillEditor() (adminAgentPrivateSkillRegistry, error) {
	registry, ok := s.deps.Registry.(adminAgentPrivateSkillRegistry)
	if !ok || registry == nil {
		return nil, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent-private skill editing is not configured")
	}
	return registry, nil
}

func (s *Server) handleAdminAgentPrivateSkillImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, catalog.EditableSkillMaxUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(catalog.EditableSkillMaxUploadBytes); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			s.writeAgentHTTPResponse(w, nil, mapPrivateSkillEditError(catalog.ErrSkillArchiveUploadTooLarge))
		} else {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid multipart form"))
		}
		return
	}
	agentKey := strings.TrimSpace(r.FormValue("agentKey"))
	if agentKey == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "agentKey is required"))
		return
	}
	file, header, err := pickSkillArchiveUpload(r.MultipartForm)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	defer file.Close()
	if !strings.EqualFold(filepath.Ext(strings.TrimSpace(header.Filename)), ".zip") || header.Size <= 0 {
		s.writeAgentHTTPResponse(w, nil, mapPrivateSkillEditError(catalog.ErrSkillArchiveInvalid))
		return
	}
	if header.Size > catalog.EditableSkillMaxUploadBytes {
		s.writeAgentHTTPResponse(w, nil, mapPrivateSkillEditError(catalog.ErrSkillArchiveUploadTooLarge))
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	if key == "" {
		key, err = catalog.DetectEditableSkillArchiveKey(file, header.Size)
		if err != nil {
			s.writeAgentHTTPResponse(w, nil, mapPrivateSkillEditError(err))
			return
		}
	}
	response, err := s.importAdminAgentPrivateSkill(r.Context(), agentKey, key, file, header.Size, parseLooseBool(r.FormValue("confirmCenterOverride")))
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminAgentPrivateSkillDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteAdminAgentPrivateSkillRequest
	if err := decodeStrictJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.deleteAdminAgentPrivateSkill(r.Context(), req.AgentKey, req.Key)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) importAdminAgentPrivateSkill(ctx context.Context, agentKey, key string, source io.ReaderAt, size int64, confirmCenterOverride bool) (api.AdminAgentDetailResponse, error) {
	s.adminAgentMutationMu.Lock()
	defer s.adminAgentMutationMu.Unlock()
	editor, err := s.adminAgentPrivateSkillEditor()
	if err != nil {
		return api.AdminAgentDetailResponse{}, err
	}
	mutation, err := editor.BeginImportEditableAgentPrivateSkillArchive(agentKey, key, source, size, confirmCenterOverride)
	if err != nil {
		return api.AdminAgentDetailResponse{}, mapPrivateSkillEditError(err)
	}
	finalized := false
	defer func() {
		if finalized {
			return
		}
		_ = editor.RollbackEditableAgentPrivateSkillMutation(mutation)
		_ = s.reloadAgentCatalog(context.WithoutCancel(ctx))
	}()
	if err := s.reloadAgentCatalog(ctx); err != nil {
		rollbackErr := editor.RollbackEditableAgentPrivateSkillMutation(mutation)
		finalized = true
		if rollbackErr != nil {
			return api.AdminAgentDetailResponse{}, fmtPrivateSkillRollbackError(err, rollbackErr)
		}
		_ = s.reloadAgentCatalog(context.WithoutCancel(ctx))
		return api.AdminAgentDetailResponse{}, err
	}
	if err := editor.CommitEditableAgentPrivateSkillMutation(mutation); err != nil {
		rollbackErr := editor.RollbackEditableAgentPrivateSkillMutation(mutation)
		finalized = true
		_ = s.reloadAgentCatalog(context.WithoutCancel(ctx))
		if rollbackErr != nil {
			return api.AdminAgentDetailResponse{}, fmtPrivateSkillRollbackError(err, rollbackErr)
		}
		return api.AdminAgentDetailResponse{}, err
	}
	finalized = true
	return s.adminAgentDetail(strings.TrimSpace(agentKey))
}

func (s *Server) deleteAdminAgentPrivateSkill(ctx context.Context, agentKey, key string) (api.AdminAgentDetailResponse, error) {
	s.adminAgentMutationMu.Lock()
	defer s.adminAgentMutationMu.Unlock()
	editor, err := s.adminAgentPrivateSkillEditor()
	if err != nil {
		return api.AdminAgentDetailResponse{}, err
	}
	mutation, err := editor.BeginDeleteEditableAgentPrivateSkill(agentKey, key)
	if err != nil {
		return api.AdminAgentDetailResponse{}, mapPrivateSkillEditError(err)
	}
	finalized := false
	defer func() {
		if finalized {
			return
		}
		_ = editor.RollbackEditableAgentPrivateSkillMutation(mutation)
		_ = s.reloadAgentCatalog(context.WithoutCancel(ctx))
	}()
	if err := s.reloadAgentCatalog(ctx); err != nil {
		rollbackErr := editor.RollbackEditableAgentPrivateSkillMutation(mutation)
		finalized = true
		if rollbackErr != nil {
			return api.AdminAgentDetailResponse{}, fmtPrivateSkillRollbackError(err, rollbackErr)
		}
		_ = s.reloadAgentCatalog(context.WithoutCancel(ctx))
		return api.AdminAgentDetailResponse{}, err
	}
	if err := editor.CommitEditableAgentPrivateSkillMutation(mutation); err != nil {
		rollbackErr := editor.RollbackEditableAgentPrivateSkillMutation(mutation)
		finalized = true
		_ = s.reloadAgentCatalog(context.WithoutCancel(ctx))
		if rollbackErr != nil {
			return api.AdminAgentDetailResponse{}, fmtPrivateSkillRollbackError(err, rollbackErr)
		}
		return api.AdminAgentDetailResponse{}, err
	}
	finalized = true
	return s.adminAgentDetail(strings.TrimSpace(agentKey))
}

func fmtPrivateSkillRollbackError(reloadErr, rollbackErr error) error {
	return newAgentStatusError(http.StatusInternalServerError, "rollback_failed", "reload agent catalog: "+reloadErr.Error()+"; rollback agent-private skill: "+rollbackErr.Error())
}

func mapPrivateSkillEditError(err error) error {
	if errors.Is(err, catalog.ErrAgentPrivateSkillDirectoryRequired) {
		return newAgentStatusError(http.StatusConflict, "directory_agent_required", err.Error())
	}
	if errors.Is(err, catalog.ErrAgentPrivateSkillOverrideConfirm) {
		return newAgentStatusErrorWithData(http.StatusConflict, "center_skill_override_confirmation_required", err.Error(), map[string]any{"requiresConfirmation": true})
	}
	return mapSkillEditError(err)
}
