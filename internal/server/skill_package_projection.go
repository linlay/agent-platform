package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
)

// Package records remain on disk; this projection does not persist another index.
func skillPackageOwners(registry adminSkillRegistry) (map[string]string, error) {
	records, err := registry.EditableSkillPackages()
	if err != nil {
		return nil, mapSkillEditError(err)
	}
	owners := make(map[string]string)
	for _, record := range records {
		for _, skill := range record.Skills {
			owners[skill.ID] = record.ID
		}
	}
	return owners, nil
}

func (s *Server) listSkillPackageSummaries() ([]api.AdminSkillPackageResponse, error) {
	registry, err := s.adminSkillRegistry()
	if err != nil {
		return nil, err
	}
	records, err := registry.EditableSkillPackages()
	if err != nil {
		return nil, mapSkillEditError(err)
	}
	response := make([]api.AdminSkillPackageResponse, 0, len(records))
	if len(records) == 0 {
		return response, nil
	}
	installed, err := registry.AdminSkills()
	if err != nil {
		return nil, mapSkillEditError(err)
	}
	ready := make(map[string]bool, len(installed))
	for _, skill := range installed {
		ready[skill.Key] = skill.Status == catalog.AdminSkillStatusReady
	}
	for _, record := range records {
		summary := adminSkillPackageResponse(record)
		for _, skill := range record.Skills {
			if !ready[skill.ID] {
				summary.Status = "incomplete"
				summary.MissingSkillIDs = append(summary.MissingSkillIDs, skill.ID)
			}
		}
		response = append(response, summary)
	}
	return response, nil
}

func (s *Server) listAgentSkillPackages() ([]api.AgentSkillPackageResponse, error) {
	// Alternative registries without a package store retain the old flat response.
	if _, ok := s.deps.Registry.(adminSkillRegistry); !ok {
		return nil, nil
	}
	packages, err := s.listSkillPackageSummaries()
	if err != nil {
		return nil, err
	}
	available := make(map[string]bool)
	for _, skill := range s.deps.Registry.Skills("") {
		if !connector.IsReservedSkill(skill.Key) {
			available[skill.Key] = true
		}
	}
	result := make([]api.AgentSkillPackageResponse, 0, len(packages))
	for _, item := range packages {
		p := api.AgentSkillPackageResponse{
			ID: item.ID, Name: firstNonBlank(item.Name, item.ID), Presentation: item.Presentation, Description: item.Description, Triggers: item.Triggers,
			Status: item.Status, Skills: []api.AdminSkillPackageSkill{}, MissingSkillIDs: []string{},
		}
		missing := make(map[string]bool, len(item.MissingSkillIDs))
		for _, id := range item.MissingSkillIDs {
			missing[id] = true
		}
		for _, skill := range item.Skills {
			// A package record can never make a connector or Agent-private skill selectable.
			if connector.IsReservedSkill(skill.ID) {
				p.Status = "incomplete"
				continue
			}
			if missing[skill.ID] || !available[skill.ID] {
				p.Status = "incomplete"
				p.MissingSkillIDs = append(p.MissingSkillIDs, skill.ID)
				continue
			}
			p.Skills = append(p.Skills, api.AdminSkillPackageSkill{ID: skill.ID, Version: skill.Version})
		}
		result = append(result, p)
	}
	return result, nil
}
