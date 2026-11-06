package acp

import (
	"agent-platform/internal/agentengine"
	dto "agent-platform/internal/contracts/agentengine"
	"context"
	"encoding/json"
)

func configOptions(raw []json.RawMessage) ([]dto.ExternalConfigOption, error) {
	if len(raw) > 64 {
		return nil, agentengine.Error("invalid_config_selection")
	}
	result := make([]dto.ExternalConfigOption, 0, len(raw))
	seen := map[string]bool{}
	for _, data := range raw {
		var option struct {
			ID           string            `json:"id"`
			Name         string            `json:"name"`
			Category     string            `json:"category"`
			Type         string            `json:"type"`
			CurrentValue json.RawMessage   `json:"currentValue"`
			Options      []json.RawMessage `json:"options"`
		}
		if json.Unmarshal(data, &option) != nil || option.ID == "" || option.Name == "" || option.Type == "" || seen[option.ID] {
			return nil, agentengine.Error("invalid_config_selection")
		}
		seen[option.ID] = true
		// Only verified semantic categories have controls. Product-specific IDs
		// and broad model_config options do not establish supported semantics.
		if option.Type != "select" || (option.Category != "model" && option.Category != "mode" && option.Category != "thought_level") {
			continue
		}
		choices := []dto.ConfigChoice{}
		for _, item := range option.Options {
			var choice struct {
				Value   *string            `json:"value"`
				Name    string             `json:"name"`
				Group   *string            `json:"group"`
				Options []dto.ConfigChoice `json:"options"`
			}
			if json.Unmarshal(item, &choice) != nil {
				return nil, agentengine.Error("invalid_config_selection")
			}
			if choice.Group != nil && choice.Value == nil {
				for _, value := range choice.Options {
					value.Group = choice.Group
					choices = append(choices, value)
				}
			} else if choice.Value != nil && choice.Group == nil {
				choices = append(choices, dto.ConfigChoice{Value: *choice.Value, Name: choice.Name})
			} else {
				return nil, agentengine.Error("invalid_config_selection")
			}
		}
		values := map[string]bool{}
		for _, choice := range choices {
			if values[choice.Value] {
				return nil, agentengine.Error("invalid_config_selection")
			}
			values[choice.Value] = true
		}
		normalized := map[string]any{"id": option.ID, "name": option.Name, "type": "select", "currentValue": option.CurrentValue, "choices": choices}
		if option.Category != "" {
			normalized["category"] = option.Category
		}
		item, err := json.Marshal(normalized)
		if err != nil || dto.Validate("ExternalConfigOption", item) != nil {
			return nil, agentengine.Error("invalid_config_selection")
		}
		result = append(result, item)
	}
	return result, nil
}

func validateSelections(options []dto.ExternalConfigOption, selections []dto.ConfigSelection) error {
	seen := map[string]bool{}
	for _, selection := range selections {
		if seen[selection.ConfigID] {
			return agentengine.Error("invalid_config_selection")
		}
		seen[selection.ConfigID] = true
		var value string
		if json.Unmarshal(selection.Value, &value) != nil {
			return agentengine.Error("invalid_config_selection")
		}
		valid := false
		for _, raw := range options {
			var option struct {
				ID      string             `json:"id"`
				Choices []dto.ConfigChoice `json:"choices"`
			}
			json.Unmarshal(raw, &option)
			if option.ID == selection.ConfigID {
				for _, choice := range option.Choices {
					if choice.Value == value {
						valid = true
					}
				}
			}
		}
		if !valid {
			return agentengine.Error("invalid_config_selection")
		}
	}
	return nil
}

func confirmedSelections(options []dto.ExternalConfigOption) []dto.ConfigSelection {
	selections := make([]dto.ConfigSelection, 0, len(options))
	for _, raw := range options {
		var value struct {
			ID           string          `json:"id"`
			CurrentValue json.RawMessage `json:"currentValue"`
		}
		json.Unmarshal(raw, &value)
		selections = append(selections, dto.ConfigSelection{ConfigID: value.ID, Value: value.CurrentValue})
	}
	return selections
}

func (s *Sessions) Configure(ctx context.Context, update dto.ConfigUpdate) (dto.PreparedSession, error) {
	data, err := json.Marshal(update)
	if err != nil || dto.Validate("ConfigUpdate", data) != nil {
		return dto.PreparedSession{}, agentengine.Error("invalid_arguments")
	}
	e, err := s.acquire(update.ChatID)
	if err != nil {
		return dto.PreparedSession{}, err
	}
	defer s.release(update.ChatID, e)
	if err = s.ready(e, update.PreparationID, update.ConfigRevision); err != nil {
		return dto.PreparedSession{}, err
	}
	e.mu.Lock()
	err = validateSelections(e.prepared.ConfigOptions, update.Selections)
	e.mu.Unlock()
	if err != nil {
		return dto.PreparedSession{}, err
	}
	if err = s.applySelections(ctx, e, update.Selections, true); err != nil {
		return dto.PreparedSession{}, err
	}
	return s.Inspect(update.ChatID)
}
func (s *Sessions) applySelections(ctx context.Context, e *session, selections []dto.ConfigSelection, persist bool) error {
	// Restored model/mode selections can change the choices of later options.
	// Validate each persisted selection against the preceding confirmed response.
	for _, selection := range selections {
		e.mu.Lock()
		err := validateSelections(e.prepared.ConfigOptions, []dto.ConfigSelection{selection})
		e.configNotified = false
		e.mu.Unlock()
		if err != nil {
			return err
		}
		raw, err := controlRequest(ctx, e.process, "session/set_config_option", map[string]any{"sessionId": e.binding.ACPSessionID, "configId": selection.ConfigID, "value": selection.Value})
		if err != nil {
			e.process.Close()
			return err
		}
		var result struct {
			ConfigOptions []json.RawMessage `json:"configOptions"`
		}
		if json.Unmarshal(raw, &result) != nil {
			e.process.Close()
			return agentengine.Error("rpc_connection_lost")
		}
		options, err := configOptions(result.ConfigOptions)
		if err != nil {
			e.process.Close()
			return err
		}
		e.mu.Lock()
		if e.configNotified {
			options = e.prepared.ConfigOptions
		}
		confirmed := false
		for _, option := range options {
			var value struct {
				ID           string          `json:"id"`
				CurrentValue json.RawMessage `json:"currentValue"`
			}
			json.Unmarshal(option, &value)
			var a, b string
			json.Unmarshal(value.CurrentValue, &a)
			json.Unmarshal(selection.Value, &b)
			if value.ID == selection.ConfigID && a == b {
				confirmed = true
			}
		}
		if !confirmed {
			e.mu.Unlock()
			e.process.Close()
			return agentengine.Error("invalid_config_selection")
		}
		e.binding.ConfigSelection = confirmedSelections(options)
		e.prepared.ConfigOptions = options
		e.prepared.ConfigRevision++
		e.mu.Unlock()
		if persist {
			if err = s.saveBinding(e); err != nil {
				e.process.Close()
				return err
			}
		}
	}
	return nil
}
