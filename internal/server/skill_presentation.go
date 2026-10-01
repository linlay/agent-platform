package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
)

// Resolve presentation on response copies, keeping the shared catalog and raw
// SKILL.md content independent of each HTTP request / WebSocket connection.
func localizeSkillResponse(locale string, value any) any {
	switch v := value.(type) {
	case api.AdminSkillsResponse:
		v.Skills = localizeSkillResponse(locale, v.Skills).([]api.AdminSkillSummary)
		v.Packages = localizeSkillResponse(locale, v.Packages).([]api.AdminSkillPackageResponse)
		return v
	case api.AdminAgentDetailResponse:
		v.PrivateSkills = append([]api.AdminAgentPrivateSkill{}, v.PrivateSkills...)
		for i := range v.PrivateSkills {
			s := &v.PrivateSkills[i]
			s.Presentation, s.Description = s.Presentation.Resolve(locale, s.Name, s.ID, s.Description)
		}
		return v
	case api.AgentSkillsResponse:
		v.Packages = append([]api.AgentSkillPackageResponse{}, v.Packages...)
		for i := range v.Packages {
			p := &v.Packages[i]
			p.Presentation, p.Description = p.Presentation.Resolve(locale, p.Name, p.ID, p.Description)
		}
		v.Skills = append([]api.AgentSkillResponse{}, v.Skills...)
		for i := range v.Skills {
			s := &v.Skills[i]
			s.Presentation, s.Description = s.Presentation.Resolve(locale, s.Name, s.ID, s.Description)
		}
		return v
	case api.AgentDetailResponse:
		v.Skills = append([]api.AgentDetailSkill{}, v.Skills...)
		for i := range v.Skills {
			s := &v.Skills[i]
			s.Presentation, s.Description = s.Presentation.Resolve(locale, s.Name, s.ID, s.Description)
		}
		return v
	case api.AdminSkillSummary:
		v.Presentation, v.Description = v.Presentation.Resolve(locale, v.Name, v.ID, v.Description)
		return v
	case []api.AdminSkillSummary:
		out := make([]api.AdminSkillSummary, len(v))
		for i := range v {
			out[i] = localizeSkillResponse(locale, v[i]).(api.AdminSkillSummary)
		}
		return out
	case []api.SkillSummary:
		out := append([]api.SkillSummary{}, v...)
		for i := range out {
			s := &out[i]
			s.Presentation, s.Description = s.Presentation.Resolve(locale, s.Name, s.ID, s.Description)
		}
		return out
	case api.AdminSkillDetailResponse:
		v.Skill = localizeSkillResponse(locale, v.Skill).(api.AdminSkillSummary)
		return v
	case api.AdminSkillPackageResponse:
		v.Presentation, v.Description = v.Presentation.Resolve(locale, v.Name, v.ID, v.Description)
		return v
	case []api.AdminSkillPackageResponse:
		out := append([]api.AdminSkillPackageResponse{}, v...)
		for i := range out {
			out[i] = localizeSkillResponse(locale, out[i]).(api.AdminSkillPackageResponse)
		}
		return out
	case api.AdminSkillImportResponse:
		if v.Package != nil {
			p := localizeSkillResponse(locale, *v.Package).(api.AdminSkillPackageResponse)
			v.Package = &p
		}
		if v.AdminSkillDetailResponse != nil {
			detail := localizeSkillResponse(locale, *v.AdminSkillDetailResponse).(api.AdminSkillDetailResponse)
			v.AdminSkillDetailResponse = &detail
		}
		return v
	case api.AdminSkillMutationResponse:
		if v.Skill != nil {
			skill := localizeSkillResponse(locale, *v.Skill).(api.AdminSkillSummary)
			v.Skill = &skill
		}
		return v
	case []catalog.ConnectorSkillSummary:
		out := append([]catalog.ConnectorSkillSummary{}, v...)
		for i := range out {
			s := &out[i]
			s.Presentation, s.Description = s.Presentation.Resolve(locale, s.Name, s.ID, s.Description)
		}
		return out
	case catalog.ConnectorSkillDetail:
		v.Skill.Presentation, v.Skill.Description = v.Skill.Presentation.Resolve(locale, v.Skill.Name, v.Skill.ID, v.Skill.Description)
		return v
	}
	return value
}
