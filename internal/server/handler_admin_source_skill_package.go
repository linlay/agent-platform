package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

type skillPackageManifestRegistry interface {
	ReadEditableSkillPackageManifest(string) (catalog.EditableSkillFileContent, error)
	BeginUpdateEditableSkillPackageManifest(string, string, string) (*catalog.EditableSkillPackageMutation, catalog.SkillPackageRecord, error)
}

func (s *Server) readAdminSkillPackageTextSource(target api.AdminSourceTarget) (api.AdminSourceResponse, error) {
	registry, ok := s.deps.Registry.(skillPackageManifestRegistry)
	if !ok {
		return api.AdminSourceResponse{}, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "skill package editor is not configured")
	}
	file, err := registry.ReadEditableSkillPackageManifest(target.Key)
	if err != nil {
		return api.AdminSourceResponse{}, mapSkillEditError(err)
	}
	return adminSourceFromSkillFile(target, filepath.Join(s.deps.Config.Paths.SkillsCenterDir, target.Key, "package.json"), file), nil
}

func (s *Server) writeAdminSkillPackageTextSource(ctx context.Context, target api.AdminSourceTarget, content, baseSHA256 string) (api.AdminSourceResponse, error) {
	registry, ok := s.deps.Registry.(skillPackageManifestRegistry)
	if !ok {
		return api.AdminSourceResponse{}, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "skill package editor is not configured")
	}
	if strings.TrimSpace(baseSHA256) == "" {
		return api.AdminSourceResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "baseSha256 is required")
	}
	return withCatalogDirectoryTransaction(ctx, s, "skills", func(ctx context.Context) (api.AdminSourceResponse, error) {
		mutation, _, err := registry.BeginUpdateEditableSkillPackageManifest(target.Key, content, baseSHA256)
		if err != nil {
			return api.AdminSourceResponse{}, mapSkillEditError(err)
		}
		if err := s.reloadAdminSkills(ctx); err != nil {
			return api.AdminSourceResponse{}, rollbackSkillPackageMutation(ctx, s, mutation, err)
		}
		result, err := s.readAdminSkillPackageTextSource(target)
		if err != nil {
			return api.AdminSourceResponse{}, rollbackSkillPackageMutation(ctx, s, mutation, err)
		}
		if err := mutation.Commit(); err != nil {
			return api.AdminSourceResponse{}, fmt.Errorf("commit skill package metadata: %w", err)
		}
		return result, nil
	})
}
