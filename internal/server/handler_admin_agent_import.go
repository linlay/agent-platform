package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

type adminAgentArchiveRegistry interface {
	BeginImportEditableAgentArchive(source io.ReaderAt, size int64, overwrite bool) (*catalog.EditableAgentArchiveMutation, error)
	RollbackEditableAgentArchiveMutation(mutation *catalog.EditableAgentArchiveMutation) error
	CommitEditableAgentArchiveMutation(mutation *catalog.EditableAgentArchiveMutation) error
}

func (s *Server) adminAgentArchiveEditor() (adminAgentArchiveRegistry, error) {
	registry, ok := s.deps.Registry.(adminAgentArchiveRegistry)
	if !ok || registry == nil {
		return nil, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "agent archive import is not configured")
	}
	return registry, nil
}

func (s *Server) handleAdminAgentImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, catalog.EditableAgentMaxArchiveUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(catalog.EditableAgentMaxArchiveUploadBytes); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			s.writeAgentHTTPResponse(w, nil, mapAgentArchiveEditError(catalog.ErrAgentArchiveUploadTooLarge))
		} else {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid multipart form"))
		}
		return
	}
	overwrite := false
	if raw := strings.TrimSpace(r.FormValue("overwrite")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "overwrite must be a boolean"))
			return
		}
		overwrite = parsed
	}
	file, header, err := pickSkillArchiveUpload(r.MultipartForm)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}
	defer file.Close()
	if !strings.EqualFold(filepath.Ext(strings.TrimSpace(header.Filename)), ".zip") || header.Size <= 0 {
		s.writeAgentHTTPResponse(w, nil, mapAgentArchiveEditError(catalog.ErrAgentArchiveInvalid))
		return
	}
	if header.Size > catalog.EditableAgentMaxArchiveUploadBytes {
		s.writeAgentHTTPResponse(w, nil, mapAgentArchiveEditError(catalog.ErrAgentArchiveUploadTooLarge))
		return
	}
	response, err := s.importAdminAgentArchive(r.Context(), file, header.Size, overwrite)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) importAdminAgentArchive(ctx context.Context, source io.ReaderAt, size int64, overwrite bool) (api.AdminAgentDetailResponse, error) {
	s.adminAgentMutationMu.Lock()
	defer s.adminAgentMutationMu.Unlock()
	editor, err := s.adminAgentArchiveEditor()
	if err != nil {
		return api.AdminAgentDetailResponse{}, err
	}
	mutation, err := editor.BeginImportEditableAgentArchive(source, size, overwrite)
	if err != nil {
		return api.AdminAgentDetailResponse{}, mapAgentArchiveEditError(err)
	}
	finalized := false
	defer func() {
		if finalized {
			return
		}
		_ = editor.RollbackEditableAgentArchiveMutation(mutation)
		_ = s.reloadAgentCatalog(context.WithoutCancel(ctx))
	}()

	if err := s.reloadAgentCatalog(ctx); err != nil {
		finalized = true
		return api.AdminAgentDetailResponse{}, s.rollbackAgentArchiveImport(ctx, editor, mutation, err)
	}
	detail, err := s.adminAgentDetail(mutation.Key)
	if err != nil {
		finalized = true
		return api.AdminAgentDetailResponse{}, s.rollbackAgentArchiveImport(ctx, editor, mutation, err)
	}
	if err := editor.CommitEditableAgentArchiveMutation(mutation); err != nil {
		finalized = true
		return api.AdminAgentDetailResponse{}, s.rollbackAgentArchiveImport(ctx, editor, mutation, err)
	}
	finalized = true
	return detail, nil
}

func (s *Server) rollbackAgentArchiveImport(ctx context.Context, editor adminAgentArchiveRegistry, mutation *catalog.EditableAgentArchiveMutation, cause error) error {
	rollbackErr := editor.RollbackEditableAgentArchiveMutation(mutation)
	reloadErr := s.reloadAgentCatalog(context.WithoutCancel(ctx))
	if rollbackErr == nil && reloadErr == nil {
		return cause
	}
	data := map[string]any{
		"code":     "rollback_failed",
		"agentKey": mutation.Key,
	}
	message := "agent archive import failed: " + cause.Error()
	if rollbackErr != nil {
		data["rollbackError"] = rollbackErr.Error()
		message += "; rollback agent source: " + rollbackErr.Error()
	}
	if reloadErr != nil {
		data["reloadError"] = reloadErr.Error()
		message += "; reload restored catalog: " + reloadErr.Error()
	}
	return newAgentStatusErrorWithData(http.StatusInternalServerError, "rollback_failed", message, data)
}

func mapAgentArchiveEditError(err error) error {
	if err == nil {
		return nil
	}
	var conflict *catalog.AgentArchiveConflictError
	if errors.As(err, &conflict) {
		return newAgentStatusErrorWithData(http.StatusConflict, "agent_exists", err.Error(), map[string]any{
			"code":              "agent_exists",
			"agentKey":          conflict.Key,
			"existingName":      conflict.Name,
			"overwriteRequired": true,
		})
	}
	var validation *catalog.AgentArchiveValidationError
	if errors.As(err, &validation) {
		diagnostics := make([]api.AdminAgentDiagnostic, 0, len(validation.Diagnostics))
		for _, diagnostic := range validation.Diagnostics {
			diagnostics = append(diagnostics, api.AdminAgentDiagnostic{
				Severity:   "error",
				Code:       diagnostic.Code,
				Message:    diagnostic.Message,
				SourcePath: diagnostic.SourcePath,
			})
		}
		return newAgentStatusErrorWithData(http.StatusUnprocessableEntity, "invalid_archive", validation.Error(), map[string]any{
			"code":        "invalid_archive",
			"diagnostics": diagnostics,
		})
	}
	switch {
	case errors.Is(err, catalog.ErrAgentArchiveTooLarge), errors.Is(err, catalog.ErrAgentArchiveUploadTooLarge), errors.Is(err, catalog.ErrAgentArchiveTooManyFiles):
		return newAgentStatusErrorWithData(http.StatusRequestEntityTooLarge, "payload_too_large", err.Error(), map[string]any{"code": "payload_too_large"})
	case errors.Is(err, catalog.ErrAgentArchiveInvalid):
		return newAgentStatusErrorWithData(http.StatusUnsupportedMediaType, "unsupported_media_type", err.Error(), map[string]any{"code": "unsupported_media_type"})
	case errors.Is(err, catalog.ErrAgentSourceSymlink):
		return newAgentStatusErrorWithData(http.StatusForbidden, "forbidden", err.Error(), map[string]any{"code": "forbidden"})
	default:
		return mapAgentEditError(err)
	}
}
