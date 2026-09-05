package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"agent-platform/internal/adminsource"
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

func (s *Server) adminAgentArchiveEditor() (adminsource.AgentArchiveEditor, error) {
	registry, ok := s.deps.Registry.(adminsource.AgentArchiveEditor)
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
	editor, err := s.adminAgentArchiveEditor()
	if err != nil {
		return api.AdminAgentDetailResponse{}, err
	}
	var detail api.AdminAgentDetailResponse
	_, err = s.adminSources.ImportAgentArchive(ctx, editor, source, size, overwrite, s.reloadAgentCatalog, func(key string) error {
		resolved, detailErr := s.adminAgentDetail(key)
		detail = resolved
		return detailErr
	})
	if err == nil {
		return detail, nil
	}
	return api.AdminAgentDetailResponse{}, mapAdminArchiveImportTransactionError(err)
}

func mapAdminArchiveImportTransactionError(err error) error {
	var beginErr *adminsource.ArchiveBeginError
	if errors.As(err, &beginErr) {
		return mapAgentArchiveEditError(beginErr.Cause)
	}
	var rollbackErr *adminsource.ArchiveRollbackError
	if errors.As(err, &rollbackErr) {
		data := map[string]any{"code": "rollback_failed", "agentKey": rollbackErr.AgentKey}
		if rollbackErr.RollbackErr != nil {
			data["rollbackError"] = rollbackErr.RollbackErr.Error()
		}
		if rollbackErr.ReloadErr != nil {
			data["reloadError"] = rollbackErr.ReloadErr.Error()
		}
		return newAgentStatusErrorWithData(http.StatusInternalServerError, "rollback_failed", rollbackErr.Error(), data)
	}
	return err
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
