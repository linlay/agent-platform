package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

const skillPackageRequestOverheadBytes int64 = 1 << 20

func (s *Server) handleAdminSkillPackages(w http.ResponseWriter, _ *http.Request) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	records, err := registry.EditableSkillPackages()
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, mapSkillEditError(err))
		return
	}
	response := make([]api.AdminSkillPackageResponse, 0, len(records))
	for _, record := range records {
		response = append(response, adminSkillPackageResponse(record))
	}
	s.writeAgentHTTPResponse(w, response, nil)
}

func (s *Server) handleAdminSkillPackageImport(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	version := strings.TrimSpace(r.URL.Query().Get("version"))
	if key == "" || version == "" {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "key and version are required"))
		return
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if contentType != "application/zip" && contentType != "application/octet-stream" {
		writeJSON(w, http.StatusUnsupportedMediaType, api.Failure(http.StatusUnsupportedMediaType, "skill package body must be a ZIP archive"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, catalog.EditableSkillPackageMaxUploadBytes+skillPackageRequestOverheadBytes)
	archive, err := os.CreateTemp("", "agent-platform-skill-package-*.zip")
	if err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}
	archivePath := archive.Name()
	defer func() {
		_ = archive.Close()
		_ = os.Remove(archivePath)
	}()

	size, err := io.Copy(archive, io.LimitReader(r.Body, catalog.EditableSkillPackageMaxUploadBytes+1))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			s.writeAgentHTTPResponse(w, nil, mapSkillEditError(catalog.ErrSkillArchiveUploadTooLarge))
		} else {
			s.writeAgentHTTPResponse(w, nil, err)
		}
		return
	}
	if size <= 0 {
		s.writeAgentHTTPResponse(w, nil, mapSkillEditError(catalog.ErrSkillArchiveInvalid))
		return
	}
	if size > catalog.EditableSkillPackageMaxUploadBytes {
		s.writeAgentHTTPResponse(w, nil, mapSkillEditError(catalog.ErrSkillArchiveUploadTooLarge))
		return
	}
	if err := archive.Sync(); err != nil {
		s.writeAgentHTTPResponse(w, nil, err)
		return
	}

	response, err := s.importAdminSkillPackage(r.Context(), key, version, archive, size)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillPackageDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteAdminSkillPackageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.deleteAdminSkillPackage(r.Context(), req.Key)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) handleAdminSkillPackageSkillDelete(w http.ResponseWriter, r *http.Request) {
	var req api.DeleteAdminSkillPackageSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "invalid payload"))
		return
	}
	response, err := s.deleteAdminSkillPackageSkill(r.Context(), req.PackageID, req.SkillID)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) importAdminSkillPackage(ctx context.Context, key string, version string, source io.ReaderAt, size int64) (api.AdminSkillPackageResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.AdminSkillPackageResponse{}, err
	}
	mutation, record, err := registry.BeginImportEditableSkillPackageArchive(key, version, source, size)
	if err != nil {
		return api.AdminSkillPackageResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.AdminSkillPackageResponse{}, rollbackSkillPackageMutation(ctx, s, mutation, err)
	}
	if err := mutation.Commit(); err != nil {
		return api.AdminSkillPackageResponse{}, fmt.Errorf("commit skill package: %w", err)
	}
	return adminSkillPackageResponse(record), nil
}

func (s *Server) deleteAdminSkillPackage(ctx context.Context, key string) (api.DeleteAdminSkillPackageResponse, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return api.DeleteAdminSkillPackageResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "key is required")
	}
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.DeleteAdminSkillPackageResponse{}, err
	}
	mutation, record, err := registry.BeginDeleteEditableSkillPackage(key)
	if err != nil {
		return api.DeleteAdminSkillPackageResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.DeleteAdminSkillPackageResponse{}, rollbackSkillPackageMutation(ctx, s, mutation, err)
	}
	if err := mutation.Commit(); err != nil {
		return api.DeleteAdminSkillPackageResponse{}, fmt.Errorf("commit skill package deletion: %w", err)
	}
	return api.DeleteAdminSkillPackageResponse{
		Key: key, Deleted: true, Skills: adminSkillPackageResponse(record).Skills,
	}, nil
}

func (s *Server) deleteAdminSkillPackageSkill(ctx context.Context, packageID, skillID string) (api.DeleteAdminSkillPackageSkillResponse, error) {
	packageID = strings.TrimSpace(packageID)
	skillID = strings.TrimSpace(skillID)
	if packageID == "" || skillID == "" {
		return api.DeleteAdminSkillPackageSkillResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "packageId and skillId are required")
	}
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return api.DeleteAdminSkillPackageSkillResponse{}, err
	}
	mutation, record, packageDeleted, err := registry.BeginDeleteEditableSkillPackageSkill(packageID, skillID)
	if err != nil {
		return api.DeleteAdminSkillPackageSkillResponse{}, mapSkillEditError(err)
	}
	if err := s.reloadAdminSkills(ctx); err != nil {
		return api.DeleteAdminSkillPackageSkillResponse{}, rollbackSkillPackageMutation(ctx, s, mutation, err)
	}
	if err := mutation.Commit(); err != nil {
		return api.DeleteAdminSkillPackageSkillResponse{}, fmt.Errorf("commit skill package child deletion: %w", err)
	}
	remaining := adminSkillPackageResponse(record).Skills
	return api.DeleteAdminSkillPackageSkillResponse{
		PackageID: packageID, SkillID: skillID, Deleted: true,
		PackageDeleted: packageDeleted, RemainingSkills: remaining,
	}, nil
}

func rollbackSkillPackageMutation(ctx context.Context, s *Server, mutation *catalog.EditableSkillPackageMutation, cause error) error {
	if rollbackErr := mutation.Rollback(); rollbackErr != nil {
		return fmt.Errorf("reload skill package: %w; rollback failed: %v", cause, rollbackErr)
	}
	_ = s.reloadAdminSkills(context.WithoutCancel(ctx))
	return cause
}

func adminSkillPackageResponse(record catalog.SkillPackageRecord) api.AdminSkillPackageResponse {
	skills := make([]api.AdminSkillPackageSkill, 0, len(record.Skills))
	for _, skill := range record.Skills {
		skills = append(skills, api.AdminSkillPackageSkill{ID: skill.ID, Version: skill.Version})
	}
	return api.AdminSkillPackageResponse{
		ID: record.ID, Version: record.Version, SHA256: record.SHA256,
		Skills: skills, InstalledAt: record.InstalledAt,
	}
}
