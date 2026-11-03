package webappbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"slices"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/chatresource"
	"agent-platform/internal/webapp"
)

type ResourceCatalog interface {
	AgentDefinition(string) (catalog.AgentDefinition, bool)
	SkillDefinition(string) (catalog.SkillDefinition, bool)
}
type ChatSummaries interface {
	Summary(string) (*chat.Summary, error)
}

func (a *Adapters) selected(scope *webapp.Scope) (catalog.AgentDefinition, error) {
	key := scope.Policy.AgentKey
	if scope.Manifest.Copilot != nil {
		key = scope.Manifest.Copilot.AgentKey
	}
	if scope.Manifest.UserConfig != nil {
		for _, f := range scope.Manifest.UserConfig.Fields {
			if f.Type == "select" && f.Source == "desktop.agents" {
				if value, ok := scope.UserConfig[f.Name].(string); ok && value != "" {
					key = value
				}
				break
			}
		}
	}
	if key == "" || key != scope.Policy.AgentKey {
		return catalog.AgentDefinition{}, sdkError("app_grant_required", 403)
	}
	def, ok := a.registry.AgentDefinition(key)
	if !ok {
		return def, sdkError("assistant_agent_unavailable", 422)
	}
	return def, nil
}

type skillItem struct {
	SkillID       string `json:"skillId"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	AgentHasSkill bool   `json:"agentHasSkill"`
}

func (a *Adapters) skills(ctx context.Context, scope *webapp.Scope, raw json.RawMessage, describe bool) (any, error) {
	var input struct {
		SkillID string `json:"skillId"`
	}
	if describe {
		if err := decode(raw, &input); err != nil {
			return nil, err
		}
	} else {
		var empty struct{}
		if err := decode(raw, &empty); err != nil {
			return nil, err
		}
	}
	def, err := a.selected(scope)
	if err != nil {
		return nil, err
	}
	items := []skillItem{}
	for _, id := range scope.Policy.Skills {
		if describe && input.SkillID != id {
			continue
		}
		skill, ok := a.registry.SkillDefinition(id)
		if !ok && slices.Contains(def.EffectiveSkills(), id) {
			skill, ok, err = def.ResolveSkillDefinition(id)
			if err != nil {
				return nil, sdkError("skill_unavailable", 503)
			}
		}
		if ok {
			items = append(items, skillItem{id, skill.Name, skill.Description, slices.Contains(def.EffectiveSkills(), id)})
		}
	}
	if describe {
		if len(items) != 1 {
			return nil, sdkError("app_grant_required", 403)
		}
		return items[0], nil
	}
	return map[string]any{"items": items}, nil
}
func (a *Adapters) artifact(ctx context.Context, scope *webapp.Scope, raw json.RawMessage, action string) (any, error) {
	var input struct {
		ChatID     string `json:"chatId"`
		RunID      string `json:"runId"`
		ArtifactID string `json:"artifactId"`
		Cursor     *int   `json:"cursor,omitempty"`
		Limit      *int   `json:"limit,omitempty"`
	}
	if err := decode(raw, &input); err != nil {
		return nil, err
	}
	if action != "artifact.list" && (input.Cursor != nil || input.Limit != nil || input.ArtifactID == "") || action == "artifact.list" && input.ArtifactID != "" {
		return nil, sdkError("invalid_args", 400)
	}
	owned, err := a.ledger.get(scope, input.ChatID, input.RunID)
	if err != nil {
		return nil, err
	}
	sum, err := a.chats.Summary(input.ChatID)
	if err != nil || sum == nil || sum.AgentKey != owned.AgentKey {
		return nil, sdkError("app_grant_required", 403)
	}
	if action == "artifact.list" {
		offset, limit := 0, 50
		if input.Cursor != nil {
			offset = *input.Cursor
		}
		if input.Limit != nil {
			limit = *input.Limit
		}
		if offset < 0 || limit < 1 || limit > 100 {
			return nil, sdkError("invalid_args", 400)
		}
		items, more, err := a.artifacts.ListArtifacts(input.ChatID, owned.RunID, offset, limit)
		if err != nil {
			return nil, artifactError(err)
		}
		result := map[string]any{"items": items}
		if more {
			result["nextCursor"] = offset + len(items)
		}
		return result, nil
	}
	if action == "artifact.get" {
		item, err := a.artifacts.GetArtifact(input.ChatID, input.ArtifactID, owned.RunID)
		return item, artifactError(err)
	}
	f, meta, err := a.artifacts.OpenArtifact(ctx, input.ChatID, input.ArtifactID, owned.RunID)
	if err != nil {
		return nil, artifactError(err)
	}
	defer f.Close()
	if meta.SizeBytes > 512<<10 {
		return nil, sdkError("artifact_too_large", 413)
	}
	body, err := io.ReadAll(io.LimitReader(f, (512<<10)+1))
	if err != nil {
		return nil, sdkError("artifact_unavailable", 503)
	}
	if len(body) > 512<<10 {
		return nil, sdkError("artifact_too_large", 413)
	}
	return map[string]any{"dataBase64": base64.StdEncoding.EncodeToString(body)}, nil
}
func artifactError(err error) error {
	if err == nil {
		return nil
	}
	if err == chatresource.ErrArtifactNotFound {
		return sdkError("artifact_not_found", 404)
	}
	if err == chatresource.ErrArtifactAmbiguous {
		return sdkError("artifact_ambiguous", 409)
	}
	return sdkError("artifact_unavailable", 503)
}
