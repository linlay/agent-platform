package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

type skillPackageManifestRegistry interface {
	ReadEditableSkillPackageManifest(string) (catalog.EditableSkillFileContent, error)
	BeginUpdateEditableSkillPackageManifest(string, string, string) (*catalog.EditableSkillPackageMutation, catalog.SkillPackageRecord, error)
}

type skillPackageManifestResponse struct {
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
}

func (s *Server) handleAdminSkillPackageManifest(w http.ResponseWriter, r *http.Request) {
	registry, ok := s.deps.Registry.(skillPackageManifestRegistry)
	if !ok {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusServiceUnavailable, "unavailable", "skill package editor is not configured"))
		return
	}
	switch r.Method {
	case http.MethodGet:
		file, err := registry.ReadEditableSkillPackageManifest(strings.TrimSpace(r.URL.Query().Get("key")))
		s.writeAgentHTTPResponse(w, skillPackageManifestResponse{Content: file.Content, SHA256: file.SHA256}, mapSkillEditError(err))
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		var req struct {
			Key        string `json:"key"`
			Content    string `json:"content"`
			BaseSHA256 string `json:"baseSha256"`
		}
		if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Key) == "" || strings.TrimSpace(req.BaseSHA256) == "" {
			writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, "key, content and baseSha256 are required"))
			return
		}
		result, err := withCatalogDirectoryTransaction(r.Context(), s, "skills", func(ctx context.Context) (skillPackageManifestResponse, error) {
			mutation, _, err := registry.BeginUpdateEditableSkillPackageManifest(req.Key, req.Content, req.BaseSHA256)
			if err != nil {
				return skillPackageManifestResponse{}, mapSkillEditError(err)
			}
			if err := s.reloadAdminSkills(ctx); err != nil {
				return skillPackageManifestResponse{}, rollbackSkillPackageMutation(ctx, s, mutation, err)
			}
			file, err := registry.ReadEditableSkillPackageManifest(req.Key)
			if err != nil {
				return skillPackageManifestResponse{}, rollbackSkillPackageMutation(ctx, s, mutation, err)
			}
			if err := mutation.Commit(); err != nil {
				return skillPackageManifestResponse{}, fmt.Errorf("commit skill package metadata: %w", err)
			}
			return skillPackageManifestResponse{Content: file.Content, SHA256: file.SHA256}, nil
		})
		s.writeAgentHTTPResponse(w, result, err)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
	}
}
