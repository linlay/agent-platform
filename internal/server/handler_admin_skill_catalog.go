package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"context"
	"net/http"
	"strings"
)

// Reuse the catalog mutation coordinator without triggering a reload or releasing watchers.
// Pins are a separate user preference snapshot, not part of a directory transaction.
func (s *Server) adminSkillsCatalog(ctx context.Context) (api.AdminSkillsResponse, error) {
	user, err := s.catalogOrderUser(ctx)
	if err != nil {
		return api.AdminSkillsResponse{}, err
	}
	return withCatalogTransaction(ctx, s, func(context.Context) (api.AdminSkillsResponse, error) {
		registry, err := s.adminSkillRegistry()
		if err != nil {
			return api.AdminSkillsResponse{}, err
		}
		skills, err := registry.AdminSkills()
		if err != nil {
			return api.AdminSkillsResponse{}, mapSkillEditError(err)
		}
		packages, err := registry.EditableSkillPackages()
		if err != nil {
			return api.AdminSkillsResponse{}, mapSkillEditError(err)
		}
		state, err := s.skillOrder.Read(user)
		if err != nil {
			return api.AdminSkillsResponse{}, err
		}
		owners := make(map[string]string)
		for _, pack := range packages {
			for _, member := range pack.Skills {
				owners[member.ID] = pack.ID
			}
		}
		result := api.AdminSkillsResponse{Skills: []api.AdminSkillSummary{}, Packages: projectSkillPackageSummaries(packages, skills), Pinned: append([]string{}, state.Order...)}
		for _, skill := range skills {
			item := buildAdminSkillSummary(skill)
			item.PackageID = owners[skill.ID]
			result.Skills = append(result.Skills, item)
		}
		return result, nil
	})
}

func (s *Server) handleAdminSkillPin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var request api.UpdateAgentSkillPinRequest
	if err := decodeJSON(r, &request); err != nil {
		s.writeAgentHTTPResponse(w, nil, newAgentStatusError(http.StatusBadRequest, "invalid_request", "invalid payload"))
		return
	}
	response, err := s.updateAdminSkillPin(r.Context(), request)
	s.writeAgentHTTPResponse(w, response, err)
}

func (s *Server) updateAdminSkillPin(ctx context.Context, request api.UpdateAgentSkillPinRequest) (api.AdminSkillPinResponse, error) {
	user, err := s.catalogOrderUser(ctx)
	if err != nil {
		return api.AdminSkillPinResponse{}, err
	}
	key := strings.ToLower(strings.TrimSpace(request.ID))
	if key == "" || len(key) > 256 || catalog.ValidateEditableSkillID(key) != nil || request.Pinned == nil {
		return api.AdminSkillPinResponse{}, newAgentStatusError(http.StatusBadRequest, "invalid_request", "id and pinned are required")
	}
	return withCatalogTransaction(ctx, s, func(context.Context) (api.AdminSkillPinResponse, error) {
		if *request.Pinned {
			registry, err := s.adminSkillRegistry()
			if err != nil {
				return api.AdminSkillPinResponse{}, err
			}
			// Only top-level management rows are pinnable. Legacy member keys can still be unpinned.
			found := false
			if !strings.Contains(key, "/") {
				packages, err := registry.EditableSkillPackages()
				if err != nil {
					return api.AdminSkillPinResponse{}, mapSkillEditError(err)
				}
				for _, pack := range packages {
					if strings.EqualFold(pack.ID, key) {
						found = true
						break
					}
				}
				if !found {
					skills, err := registry.AdminSkills()
					if err != nil {
						return api.AdminSkillPinResponse{}, mapSkillEditError(err)
					}
					for _, skill := range skills {
						if strings.EqualFold(skill.ID, key) {
							found = true
							break
						}
					}
				}
			}
			if !found {
				return api.AdminSkillPinResponse{}, newAgentStatusError(http.StatusNotFound, "skill_not_found", "skill is not available in the management catalog")
			}
		}
		state, err := s.skillOrder.SetPinned(user, key, *request.Pinned)
		if err != nil {
			return api.AdminSkillPinResponse{}, err
		}
		return api.AdminSkillPinResponse{Pinned: append([]string{}, state.Order...)}, nil
	})
}
