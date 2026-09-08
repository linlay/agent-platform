package catalog

import "agent-platform/internal/connector"

type ConnectorSkillSummary struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version,omitempty"`
	Triggers    []string `json:"triggers,omitempty"`
	Path        string   `json:"path"`
	Size        int64    `json:"size"`
	UpdatedAt   int64    `json:"updatedAt"`
}

type ConnectorSkillDetail struct {
	ConnectorID string                `json:"connectorId"`
	Skill       ConnectorSkillSummary `json:"skill"`
	Content     string                `json:"content"`
	SHA256      string                `json:"sha256"`
}

func ConnectorSkills(sources connector.Sources, id string) ([]ConnectorSkillSummary, error) {
	documents, err := sources.SkillDocuments(id)
	if err != nil {
		return nil, err
	}
	result := make([]ConnectorSkillSummary, 0, len(documents))
	for _, document := range documents {
		result = append(result, connectorSkillSummary(document))
	}
	return result, nil
}

func ReadConnectorSkill(sources connector.Sources, id, name string) (ConnectorSkillDetail, error) {
	document, err := sources.ReadSkillDocument(id, name)
	if err != nil {
		return ConnectorSkillDetail{}, err
	}
	return ConnectorSkillDetail{ConnectorID: id, Skill: connectorSkillSummary(document), Content: document.Content, SHA256: document.SHA256}, nil
}

func connectorSkillSummary(document connector.SkillDocument) ConnectorSkillSummary {
	_, description, triggers, _, version := parseSkillPromptMetadata(document.Content)
	return ConnectorSkillSummary{Name: document.Name, Description: description, Version: version, Triggers: triggers, Path: document.Path, Size: document.Size, UpdatedAt: document.UpdatedAt}
}
